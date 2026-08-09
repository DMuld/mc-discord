// minecraft_discord_notifier tails a Minecraft server log and posts selected events to Discord.
//
// Usage:
//
//	DISCORD_WEBHOOK_URL='https://discord.com/api/webhooks/...' go run . -log /path/to/logs/latest.log
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	joinRE      = regexp.MustCompile(`(?i): ([^:]+) joined the game`)
	leftRE      = regexp.MustCompile(`(?i): ([^:]+) left the game`)
	achievement = regexp.MustCompile(`(?i): ([^:]+) has made the advancement (\[[\w*\s*]*])`)
)

func main() {
	logPath := flag.String("log", "logs/latest.log", "path to the Minecraft latest.log file")
	flag.Parse()

	webhookURL := os.Getenv("DISCORD_WEBHOOK_URL")
	if webhookURL == "" {
		fmt.Fprintln(os.Stderr, "DISCORD_WEBHOOK_URL is required")
		os.Exit(1)
	}

	file, err := os.Open(*logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open log: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// Start at the end, so existing log history is not re-sent on startup.
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		fmt.Fprintf(os.Stderr, "seek log: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Watching %s\n", *logPath)
	for {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			if message := discordMessage(scanner.Text()); message != "" {
				if err := sendDiscord(webhookURL, message); err != nil {
					fmt.Fprintf(os.Stderr, "send Discord notification: %v\n", err)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "read log: %v\n", err)
		}

		// Scanner reaches EOF after currently available lines. Wait for appends.
		time.Sleep(time.Second)
	}
}

func discordMessage(line string) string {
	if match := joinRE.FindStringSubmatch(line); match != nil {
		return fmt.Sprintf("🟢 **%s** joined the Minecraft server", match[1])
	}
	if match := leftRE.FindStringSubmatch(line); match != nil {
		return fmt.Sprintf("🔴 **%s** left the Minecraft server", match[1])
	}
	if match := achievement.FindStringSubmatch(line); match != nil {
		return fmt.Sprintf("**%s** has made the advancedment **%s**", match[1], match[2])
	}
	return ""
}

func sendDiscord(webhookURL, content string) error {
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return err
	}

	resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Discord returned %s", resp.Status)
	}
	return nil
}

// Keep strings imported for easy extension with custom line matching.
var _ = strings.Contains
