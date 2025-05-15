package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Configuration
const (
	baseURL             = "http://eah-2025-ai-jam.dev.elastic.cloud:8082/api/v0" // IMPORTANT: Replace with actual API base URL
	leaderboardEndpoint = "/leaderboard"
	playerGamesEndpoint = "/players/%s/games" // %s will be playerID
	leaderboardLimit    = 100                 // Max number of leaderboard entries to fetch
	playerGamesLimit    = 50                  // Max number of games to fetch per player
	requestTimeout      = 30 * time.Second
)

// Structs for Leaderboard
type LeaderboardEntry struct {
	PlayerID  string `json:"player_id"`
	Chips     int    `json:"chips"`
	MaxChips  int    `json:"max_chips"`
	Epoch     int    `json:"epoch"`
	GameCount int    `json:"game_count"`
}

type LeaderboardResponse struct {
	Entries []LeaderboardEntry `json:"entries"`
}

// Structs for Player Games
type PlayerGameUser struct {
	Username   string `json:"username"`
	GameID     string `json:"game_id"`
	ChipsDelta int    `json:"chips_delta"`
}

type PlayerGameDetail struct {
	GameID    string                 `json:"game_id"`
	Type      string                 `json:"type"`
	Timestamp string                 `json:"timestamp"`
	GameState map[string]interface{} `json:"game_state"`
}

type PlayerGame struct {
	User PlayerGameUser   `json:"user"`
	Game PlayerGameDetail `json:"game"`
}

type PlayerGamesResponse struct {
	Games []PlayerGame `json:"games"`
}

// Helper function to make HTTP GET requests and unmarshal JSON
func getAndUnmarshal(url string, target interface{}) error {
	fmt.Printf("DEBUG: Requesting URL: %s\n", url) // DEBUG: Print URL

	client := &http.Client{Timeout: requestTimeout}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("error creating request for %s: %w", url, err)
	}

	// Add a common header that might be expected by some APIs
	req.Header.Set("Accept", "application/json")
	// You can also set a User-Agent if you suspect it matters
	// req.Header.Set("User-Agent", "MyHackathonClient/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error making GET request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	fmt.Printf("DEBUG: Received status code %d for URL: %s\n", resp.StatusCode, url) // DEBUG: Print status code

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body from %s: %w", url, err)
	}
	// DEBUG: Print raw response body
	fmt.Printf("DEBUG: Raw response body for %s:\n%s\n", url, string(bodyBytes))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received non-200 status code from %s: %d %s. Body: %s", url, resp.StatusCode, resp.Status, string(bodyBytes))
	}

	// Now try to unmarshal the bodyBytes we already read
	if err := json.Unmarshal(bodyBytes, target); err != nil {
		return fmt.Errorf("error decoding JSON from %s (status %d): %w. Body: %s", url, resp.StatusCode, err, string(bodyBytes))
	}
	return nil
}

func main() {
	fmt.Println("Fetching leaderboard...")

	// 1. Get Leaderboard
	leaderboardURL := fmt.Sprintf("%s%s?limit=%d", baseURL, leaderboardEndpoint, leaderboardLimit)
	var leaderboardData LeaderboardResponse

	err := getAndUnmarshal(leaderboardURL, &leaderboardData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching leaderboard: %v\n", err)
		os.Exit(1)
	}

	if len(leaderboardData.Entries) == 0 {
		fmt.Println("Leaderboard is empty or no entries found (check DEBUG output for raw response).")
		// We might still want to exit if the expectation is to have entries.
		// If an empty list is a valid scenario and we got a 200 OK, we might not exit.
		// For now, let's assume if it's empty after a 200 OK, it's genuinely empty.
		if leaderboardData.Entries == nil { // Distinguish between empty list and parsing failure if target wasn't populated
			fmt.Println("DEBUG: leaderboardData.Entries is nil, possibly due to earlier error or truly empty response struct.")
		}
		os.Exit(0)
	}

	fmt.Printf("Found %d players on the leaderboard (up to %d requested).\n", len(leaderboardData.Entries), leaderboardLimit)
	fmt.Println("-------------------------------------------------------------")

	// 2. For each player, get their games
	for i, playerEntry := range leaderboardData.Entries {
		fmt.Printf("\n[%d/%d] Fetching games for player: %s (Chips: %d, Games: %d)\n",
			i+1, len(leaderboardData.Entries), playerEntry.PlayerID, playerEntry.Chips, playerEntry.GameCount)

		playerGamesURL := fmt.Sprintf("%s%s?limit=%d", baseURL, fmt.Sprintf(playerGamesEndpoint, playerEntry.PlayerID), playerGamesLimit)
		var playerGamesData PlayerGamesResponse

		err := getAndUnmarshal(playerGamesURL, &playerGamesData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error fetching games for player %s: %v\n", playerEntry.PlayerID, err)
			continue
		}

		if len(playerGamesData.Games) == 0 {
			fmt.Printf("  Player %s has no game history recorded (or none within the limit of %d, check DEBUG for raw response).\n", playerEntry.PlayerID, playerGamesLimit)
			continue
		}

		fmt.Printf("  Found %d games for player %s (up to %d requested):\n", len(playerGamesData.Games), playerEntry.PlayerID, playerGamesLimit)
		for _, game := range playerGamesData.Games {
			fmt.Printf("    - Game ID: %s, Timestamp: %s, Chips Delta: %d\n",
				game.Game.GameID, game.Game.Timestamp, game.User.ChipsDelta)
		}
		fmt.Println("-------------------------------------------------------------")
	}

	fmt.Println("\nFinished processing leaderboard and player games.")
}
