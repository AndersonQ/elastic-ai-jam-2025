package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// --- Configuration ---
const (
	// IMPORTANT: Replace with the actual TCP server address and port
	tcpServerAddress = "eah-2025-ai-jam.dev.elastic.cloud:8083"

	// Number of players to attempt to create.
	// WARNING: Setting this to 1,000,000 will take a very long time and put extreme load on the server.
	// Start with a small number like 100 for testing.
	numPlayersToCreate = 100000000 // Defaulting to a smaller number for safety

	// maxConcurrentRegistrations controls how many registration attempts run in parallel.
	maxConcurrentRegistrations = 100 // Adjust based on your machine and network capacity

	baseUsername = "over"     // Usernames will be like testplayer0, testplayer1, ...
	basePassword = "password" // Passwords will be like password0, password1, ...

	// connectionTimeout is the timeout for establishing the TCP connection.
	connectionTimeout = 10 * time.Second
	// readWriteTimeout is the timeout for individual read/write operations on the socket.
	readWriteTimeout = 5 * time.Second
)

// --- Structs ---

// RegistrationMsg is sent to the server to register/login.
type RegistrationMsg struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ServerResponse is a generic structure to capture server's JSON responses.
type ServerResponse struct {
	Type    string      `json:"type,omitempty"`    // e.g., "event_player_leaderboard_entry_start"
	Event   interface{} `json:"event,omitempty"`   // Can be any JSON structure
	Code    int         `json:"code,omitempty"`    // e.g., 400 for errors
	Message string      `json:"message,omitempty"` // Error message
	GameID  string      `json:"game_id,omitempty"` // Present in some events
}

// --- Global Counters (using atomic for thread-safety) ---
var (
	successfulRegistrations int32
	failedRegistrations     int32
)

// --- Main Application ---
func main() {
	fmt.Printf("--- TCP Player Creator ---\n")
	fmt.Printf("WARNING: This script will attempt to create %d players.\n", numPlayersToCreate)
	fmt.Printf("Target TCP Server: %s\n", tcpServerAddress)
	fmt.Printf("Concurrency Level: %d\n", maxConcurrentRegistrations)
	fmt.Println("Consider starting with a much smaller number of players for initial testing.")
	fmt.Println("Press Ctrl+C to interrupt at any time (though players already registered will remain).")
	fmt.Println("-----------------------------------------")
	// Brief pause for the user to read the warning
	time.Sleep(5 * time.Second)

	var wg sync.WaitGroup
	// Semaphore to limit concurrency
	semaphore := make(chan struct{}, maxConcurrentRegistrations)

	startTime := time.Now()

	for i := 0; i < numPlayersToCreate; i++ {
		wg.Add(1)
		semaphore <- struct{}{} // Acquire a slot in the semaphore

		go registerPlayer(i, &wg, semaphore)

		// Optional: print progress periodically
		if (i+1)%100 == 0 {
			fmt.Printf("Launched registration for player %d...\n", i+1)
		}
	}

	wg.Wait() // Wait for all goroutines to finish
	close(semaphore)

	duration := time.Since(startTime)
	fmt.Println("-----------------------------------------")
	fmt.Println("All registration attempts completed.")
	fmt.Printf("Duration: %s\n", duration)
	fmt.Printf("Successful registrations: %d\n", atomic.LoadInt32(&successfulRegistrations))
	fmt.Printf("Failed registrations: %d\n", atomic.LoadInt32(&failedRegistrations))
	fmt.Printf("Total attempted: %d\n", numPlayersToCreate)
}

// registerPlayer attempts to register a single player.
func registerPlayer(id int, wg *sync.WaitGroup, semaphore chan struct{}) {
	defer wg.Done()
	defer func() { <-semaphore }() // Release slot in semaphore

	username := baseUsername + strconv.Itoa(id)
	password := basePassword + strconv.Itoa(id) // You might want a more robust password generation

	// 1. Establish TCP connection
	conn, err := net.DialTimeout("tcp", tcpServerAddress, connectionTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] Error dialing TCP server: %v\n", username, err)
		atomic.AddInt32(&failedRegistrations, 1)
		return
	}
	defer conn.Close()

	// 2. Set read/write deadlines
	if err := conn.SetDeadline(time.Now().Add(readWriteTimeout * 2)); err != nil { // Overall deadline for interaction
		fmt.Fprintf(os.Stderr, "[%s] Error setting deadline: %v\n", username, err)
		atomic.AddInt32(&failedRegistrations, 1)
		return
	}

	// 3. Prepare registration message
	regMsg := RegistrationMsg{Username: username, Password: password}
	regPayload, err := json.Marshal(regMsg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] Error marshalling registration JSON: %v\n", username, err)
		atomic.AddInt32(&failedRegistrations, 1)
		return
	}

	// 4. Send registration message (JSON object followed by newline)
	if _, err := conn.Write(append(regPayload, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] Error sending registration data: %v\n", username, err)
		atomic.AddInt32(&failedRegistrations, 1)
		return
	}

	// 5. Read server response
	// The server sends newline-delimited JSON.
	reader := bufio.NewReader(conn)
	responseLine, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] Error reading server response: %v\n", username, err)
		atomic.AddInt32(&failedRegistrations, 1)
		return
	}

	// 6. Parse server response
	var serverResp ServerResponse
	if err := json.Unmarshal([]byte(responseLine), &serverResp); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] Error unmarshalling server response '%s': %v\n", username, responseLine, err)
		atomic.AddInt32(&failedRegistrations, 1)
		return
	}

	// 7. Check response
	// According to protocol, a successful registration returns an "event_player_leaderboard_entry_start"
	if serverResp.Type == "event_player_leaderboard_entry_start" {
		// fmt.Printf("[%s] Successfully registered.\n", username) // Can be too verbose for many players
		atomic.AddInt32(&successfulRegistrations, 1)
	} else if serverResp.Code != 0 { // Assuming errors have a non-zero code
		fmt.Fprintf(os.Stderr, "[%s] Registration failed: Code %d, Message: %s\n", username, serverResp.Code, serverResp.Message)
		atomic.AddInt32(&failedRegistrations, 1)
	} else {
		fmt.Fprintf(os.Stderr, "[%s] Registration resulted in unexpected response: Type='%s', Message='%s'\n", username, serverResp.Type, serverResp.Message)
		atomic.AddInt32(&failedRegistrations, 1)
	}

	// Note: The protocol mentions the server might send other events after login if the player
	// is immediately put into a game queue or similar. This script only checks the first response.
}
