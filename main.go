// minecraft_discord_notifier tails a Minecraft server log and posts selected events to Discord.
//
// Usage:
//
//	DISCORD_WEBHOOK_URL='https://discord.com/api/webhooks/...' go run . -log /path/to/logs/latest.log
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var (
	joinRE        = regexp.MustCompile(`(?i): ([^:]+) joined the game`)
	leftRE        = regexp.MustCompile(`(?i): ([^:]+) left the game`)
	advancementRE = regexp.MustCompile(`(?i): ([^:]+) has made the advancement (\[.*\])`)

	httpClient = &http.Client{
		Timeout: 15 * time.Second,
	}
)

func main() {
	logPath := flag.String("log", "logs/latest.log", "path to the Minecraft latest.log file")
	flag.Parse()

	webhookURL := os.Getenv("DISCORD_WEBHOOK_URL")
	if webhookURL == "" {
		fmt.Fprintln(os.Stderr, "DISCORD_WEBHOOK_URL is required")
		os.Exit(1)
	}

	if err := tailLog(*logPath, webhookURL); err != nil {
		fmt.Fprintf(os.Stderr, "notifier stopped: %v\n", err)
		os.Exit(1)
	}
}

func tailLog(logPath, webhookURL string) error {
	var (
		file       *os.File
		reader     *bufio.Reader
		currentID  fileID
		startAtEnd = true
	)

	for {
		if file == nil {
			f, id, err := openLog(logPath, startAtEnd)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Fprintf(os.Stderr, "waiting for log file %s\n", logPath)
					time.Sleep(time.Second)
					continue
				}
				return err
			}

			file = f
			reader = bufio.NewReaderSize(file, 64*1024)
			currentID = id
			startAtEnd = false
			fmt.Printf("Watching %s\n", logPath)
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")

			if message := discordMessage(line); message != "" {
				if err := sendDiscord(webhookURL, message); err != nil {
					fmt.Fprintf(os.Stderr, "send Discord notification: %v\n", err)
				}
			}
		}

		switch {
		case err == nil:
			continue

		case errors.Is(err, io.EOF):
			rotated, err := logWasRotatedOrTruncated(logPath, file, currentID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "check log rotation: %v\n", err)
			} else if rotated {
				fmt.Printf("Log rotated or truncated; reopening %s\n", logPath)
				_ = file.Close()
				file = nil
				reader = nil
				continue
			}

			time.Sleep(500 * time.Millisecond)

		default:
			fmt.Fprintf(os.Stderr, "read log: %v; reopening\n", err)
			_ = file.Close()
			file = nil
			reader = nil
			time.Sleep(time.Second)
		}
	}
}

type fileID struct {
	dev uint64
	ino uint64
}

func openLog(path string, seekToEnd bool) (*os.File, fileID, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fileID{}, err
	}

	id, err := getFileID(file)
	if err != nil {
		_ = file.Close()
		return nil, fileID{}, fmt.Errorf("stat opened log: %w", err)
	}

	if seekToEnd {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			_ = file.Close()
			return nil, fileID{}, fmt.Errorf("seek log: %w", err)
		}
	}

	return file, id, nil
}

func logWasRotatedOrTruncated(path string, openFile *os.File, openID fileID) (bool, error) {
	pathInfo, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}

	pathID, err := getFileIDFromInfo(pathInfo)
	if err != nil {
		return false, err
	}

	// `latest.log` was replaced with a new file.
	if pathID != openID {
		return true, nil
	}

	// Same file path/inode, but it was truncated in place.
	position, err := openFile.Seek(0, io.SeekCurrent)
	if err != nil {
		return false, err
	}

	return pathInfo.Size() < position, nil
}

func getFileID(file *os.File) (fileID, error) {
	info, err := file.Stat()
	if err != nil {
		return fileID{}, err
	}
	return getFileIDFromInfo(info)
}

func getFileIDFromInfo(info os.FileInfo) (fileID, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, fmt.Errorf("unsupported filesystem stat type %T", info.Sys())
	}
	return fileID{
		dev: uint64(stat.Dev),
		ino: uint64(stat.Ino),
	}, nil
}

func discordMessage(line string) string {
	if match := joinRE.FindStringSubmatch(line); match != nil {
		return fmt.Sprintf("🟢 **%s** joined the Minecraft server", match[1])
	}

	if match := leftRE.FindStringSubmatch(line); match != nil {
		return fmt.Sprintf("🔴 **%s** left the Minecraft server", match[1])
	}

	if match := advancementRE.FindStringSubmatch(line); match != nil {
		return fmt.Sprintf("🏆 **%s** has made the advancement %s", match[1], match[2])
	}

	return ""
}

func sendDiscord(webhookURL, content string) error {
	payload, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return err
	}

	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)

		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPost,
			webhookURL,
			bytes.NewReader(payload),
		)
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		cancel()

		if err != nil {
			if attempt == 2 {
				return err
			}
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}

		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			wait := retryAfter(resp)
			fmt.Fprintf(os.Stderr, "Discord rate limited request; retrying in %s\n", wait)
			time.Sleep(wait)
			continue
		}

		return fmt.Errorf(
			"Discord returned %s: %s",
			resp.Status,
			strings.TrimSpace(string(responseBody)),
		)
	}

	return errors.New("Discord request failed after retries")
}

func retryAfter(resp *http.Response) time.Duration {
	if value := resp.Header.Get("Retry-After"); value != "" {
		if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds > 0 {
			return seconds
		}
	}

	var body struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.RetryAfter > 0 {
		return time.Duration(body.RetryAfter * float64(time.Second))
	}

	return 2 * time.Second
}
