package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- Configuration ---
const (
	// IMPORTANT: Replace with the actual TCP server address and port
	tcpServerAddress = "eah-2025-ai-jam.dev.elastic.cloud:8083" // Example: "game.example.com:8081"

	// Number of players to attempt to create and have play.
	// WARNING: Start with 1 for testing the game logic.
	numPlayersToCreate = 1000000 // Defaulting to 1 for testing game logic

	// maxConcurrentRegistrations controls how many sessions run in parallel.
	maxConcurrentRegistrations = 1000 // Start with 1 for testing game logic

	baseUsername = "over-"    // Usernames will be like gameplayer0, gameplayer1, ...
	basePassword = "password" // Passwords will be like password0, password1, ...

	connectionTimeout   = 10 * time.Second // For establishing TCP connection
	readWriteTimeout    = 10 * time.Second // For individual read/write ops (increased for game interaction)
	gameActivityTimeout = 60 * time.Second // Max time to wait for any game activity before assuming stall

	verboseLogging = true // Set to true to see detailed logs for one player session
)

// --- Structs ---

// RegistrationMsg is sent to the server to register/login.
type RegistrationMsg struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ActionMsg is for sending actions like "join", "bet", "fold".
type ActionMsg struct {
	Action string `json:"action"`
	Amount *int   `json:"amount,omitempty"` // Pointer to allow omitting for "join"
}

// ServerResponse is a generic structure to capture server's JSON responses.
type ServerResponse struct {
	Type    string      `json:"type,omitempty"`
	Event   interface{} `json:"event,omitempty"`
	Code    int         `json:"code,omitempty"`
	Message string      `json:"message,omitempty"`
	GameID  string      `json:"game_id,omitempty"` // Present in some events

	// Fields for action_player_bet
	Stage      string                   `json:"stage,omitempty"`
	State      ActionPlayerBetFullState `json:"state,omitempty"`
	MinimumBet int                      `json:"minimum_bet,omitempty"`
}

// PlayerStateForBet is part of the action_player_bet event.
type PlayerStateForBet struct {
	PlayerID string `json:"player_id"`
	Chips    int    `json:"chips"`
	// Hand []string `json:"hand"` // Not strictly needed for this strategy
}

// ActionPlayerBetFullState is part of the action_player_bet event.
type ActionPlayerBetFullState struct {
	Player PlayerStateForBet `json:"player"`
	// Table []string `json:"table"`
	// Players []map[string]interface{} `json:"players"` // Other players' states
}

// PlayerSessionState holds the state for a single player's game session.
type PlayerSessionState struct {
	username          string
	conn              net.Conn
	reader            *bufio.Reader
	hasPerformedAllIn bool
	logPrefix         string
}

// --- Global Counters (using atomic for thread-safety) ---
var (
	successfulRegistrations int32
	failedRegistrations     int32
	gamesJoined             int32
	allInsMade              int32
	foldsMade               int32
)

// --- Main Application ---
func main() {
	fmt.Printf("--- TCP Player Creator & Game Player ---\n")
	fmt.Printf("WARNING: This script will attempt to create %d players and have them play.\n", numPlayersToCreate)
	fmt.Printf("Target TCP Server: %s\n", tcpServerAddress)
	fmt.Printf("Concurrency Level: %d\n", maxConcurrentRegistrations)
	if verboseLogging && numPlayersToCreate > 1 {
		fmt.Println("Verbose logging is ON, but numPlayersToCreate > 1. Logs might be interleaved and hard to read.")
		fmt.Println("Consider setting numPlayersToCreate to 1 when verboseLogging is true for easier debugging.")
	}
	fmt.Println("Press Ctrl+C to interrupt.")
	fmt.Println("-----------------------------------------")

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, maxConcurrentRegistrations)
	startTime := time.Now()

	for i := 0; i < numPlayersToCreate; i++ {
		wg.Add(1)
		semaphore <- struct{}{}

		go managePlayerSession(i, &wg, semaphore)
	}

	wg.Wait()
	close(semaphore)

	duration := time.Since(startTime)
	fmt.Println("-----------------------------------------")
	fmt.Println("All player session attempts completed.")
	fmt.Printf("Duration: %s\n", duration)
	fmt.Printf("Successful registrations: %d\n", atomic.LoadInt32(&successfulRegistrations))
	fmt.Printf("Failed registrations: %d\n", atomic.LoadInt32(&failedRegistrations))
	fmt.Printf("Games Joined by players: %d\n", atomic.LoadInt32(&gamesJoined))
	fmt.Printf("All-In Bets Made: %d\n", atomic.LoadInt32(&allInsMade))
	fmt.Printf("Folds Made: %d\n", atomic.LoadInt32(&foldsMade))
	fmt.Printf("Total player sessions attempted: %d\n", numPlayersToCreate)
}

// managePlayerSession handles the entire lifecycle for one player.
func managePlayerSession(id int, wg *sync.WaitGroup, semaphore chan struct{}) {
	defer wg.Done()
	defer func() { <-semaphore }()

	playerState := &PlayerSessionState{
		username:  baseUsername + strconv.Itoa(id),
		logPrefix: fmt.Sprintf("[%s] ", baseUsername+strconv.Itoa(id)),
	}
	password := basePassword + strconv.Itoa(id)

	// 1. Establish TCP connection
	var err error
	playerState.conn, err = net.DialTimeout("tcp", tcpServerAddress, connectionTimeout)
	if err != nil {
		playerState.logVerbose("Error dialing TCP server: %v", err)
		atomic.AddInt32(&failedRegistrations, 1)
		return
	}
	defer playerState.conn.Close()
	playerState.reader = bufio.NewReader(playerState.conn)

	// 2. Register
	if !playerState.register(password) {
		return // Registration failed, error already logged and counter incremented
	}
	atomic.AddInt32(&successfulRegistrations, 1)
	playerState.logVerbose("Successfully registered.")

	// 3. Join Game
	if !playerState.joinGame() {
		return // Join game failed
	}
	atomic.AddInt32(&gamesJoined, 1)
	playerState.logVerbose("Successfully sent join action. Waiting for game events...")

	// 4. Game Interaction Loop
	playerState.gameLoop()

	playerState.logVerbose("Session ended.")
}

func (ps *PlayerSessionState) logVerbose(format string, args ...interface{}) {
	if verboseLogging || numPlayersToCreate == 1 { // Always log if only one player for easier debugging
		fmt.Printf(ps.logPrefix+format+"\n", args...)
	}
}

func (ps *PlayerSessionState) sendJSON(data interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		ps.logVerbose("Error marshalling JSON for sending: %v", err)
		return err
	}
	ps.logVerbose("Sending: %s", string(payload))
	if err := ps.conn.SetWriteDeadline(time.Now().Add(readWriteTimeout)); err != nil {
		ps.logVerbose("Error setting write deadline: %v", err)
		return err
	}
	if _, err := ps.conn.Write(append(payload, '\n')); err != nil {
		ps.logVerbose("Error sending data: %v", err)
		return err
	}
	return nil
}

func (ps *PlayerSessionState) readServerMessage() (*ServerResponse, error) {
	if err := ps.conn.SetReadDeadline(time.Now().Add(readWriteTimeout)); err != nil {
		ps.logVerbose("Error setting read deadline: %v", err)
		return nil, err
	}
	responseLine, err := ps.reader.ReadString('\n')
	if err != nil {
		// Don't log EOF or timeout errors as verbose if they are expected (e.g. end of game)
		// But for now, let's log them to see what's happening.
		ps.logVerbose("Error reading server response line: %v", err)
		return nil, err
	}
	ps.logVerbose("Received: %s", strings.TrimSpace(responseLine))

	var serverResp ServerResponse
	if err := json.Unmarshal([]byte(responseLine), &serverResp); err != nil {
		ps.logVerbose("Error unmarshalling server response '%s': %v", strings.TrimSpace(responseLine), err)
		return nil, err
	}
	return &serverResp, nil
}

func (ps *PlayerSessionState) register(password string) bool {
	regMsg := RegistrationMsg{Username: ps.username, Password: password}
	if err := ps.sendJSON(regMsg); err != nil {
		atomic.AddInt32(&failedRegistrations, 1)
		return false
	}

	resp, err := ps.readServerMessage()
	if err != nil {
		atomic.AddInt32(&failedRegistrations, 1)
		return false
	}

	if resp.Type == "event_player_leaderboard_entry_start" {
		return true
	} else if resp.Code != 0 {
		ps.logVerbose("Registration failed: Code %d, Message: %s", resp.Code, resp.Message)
		atomic.AddInt32(&failedRegistrations, 1)
		return false
	} else {
		ps.logVerbose("Registration resulted in unexpected response: Type='%s'", resp.Type)
		atomic.AddInt32(&failedRegistrations, 1)
		return false
	}
}

func (ps *PlayerSessionState) joinGame() bool {
	joinMsg := ActionMsg{Action: "join"}
	if err := ps.sendJSON(joinMsg); err != nil {
		return false // Error already logged by sendJSON
	}
	// No specific response expected immediately for "join", server will send game events.
	return true
}

func (ps *PlayerSessionState) gameLoop() {
	gameStartTime := time.Now()
	for {
		if time.Since(gameStartTime) > gameActivityTimeout {
			ps.logVerbose("Game activity timeout. Ending session.")
			return
		}

		resp, err := ps.readServerMessage()
		if err != nil {
			ps.logVerbose("Exiting game loop due to read error: %v", err)
			return // Connection likely closed or timed out
		}

		switch resp.Type {
		case "action_player_bet":
			// Check if this action is for the current player
			if resp.State.Player.PlayerID == ps.username {
				ps.logVerbose("It's my turn to bet. Stage: %s, My Chips: %d", resp.Stage, resp.State.Player.Chips)
				if !ps.hasPerformedAllIn {
					// Go all-in
					amountToBet := resp.State.Player.Chips
					if amountToBet <= 0 { // Cannot bet 0 or less, must be at least minimum or fold
						ps.logVerbose("Chips are %d, cannot make a positive bet. Will fold instead of all-in.", amountToBet)
						betAction := ActionMsg{Action: "bet", Amount: pint(-1)} // Fold
						if err := ps.sendJSON(betAction); err != nil {
							ps.logVerbose("Error sending fold action: %v. Exiting.", err)
							return
						}
						atomic.AddInt32(&foldsMade, 1)
						// ps.hasPerformedAllIn = true; // Consider this "all-in strategy" attempt complete
					} else {
						ps.logVerbose("Going all-in with %d chips.", amountToBet)
						betAction := ActionMsg{Action: "bet", Amount: pint(amountToBet)}
						if err := ps.sendJSON(betAction); err != nil {
							ps.logVerbose("Error sending all-in bet: %v. Exiting.", err)
							return
						}
						atomic.AddInt32(&allInsMade, 1)
						ps.hasPerformedAllIn = true
					}
				} else {
					// Fold
					ps.logVerbose("Already performed all-in, now folding.")
					foldAction := ActionMsg{Action: "bet", Amount: pint(-1)} // amount < 0 is fold
					if err := ps.sendJSON(foldAction); err != nil {
						ps.logVerbose("Error sending fold action: %v. Exiting.", err)
						return
					}
					atomic.AddInt32(&foldsMade, 1)
				}
			} else {
				// ps.logVerbose("Action_player_bet received, but not for me (for %s).", resp.State.Player.PlayerID)
			}
		case "event_game_over", "event_player_leaderboard_entry_end":
			ps.logVerbose("Received terminal event: %s. Ending session.", resp.Type)
			if resp.Type == "event_game_over" && verboseLogging {
				eventData, _ := json.Marshal(resp.Event)
				ps.logVerbose("Game Over Event Data: %s", string(eventData))
			}
			return
		case "event_pot_won":
			// Check if we are out of chips
			if ps.hasPerformedAllIn { // Only relevant if we've been playing
				// The event_pot_won structure needs to be parsed to find our player's chip count
				// For simplicity, we rely on action_player_bet or game_over for chip status.
				// ps.logVerbose("Pot won event. Current chips might have changed.")
			}
		case "": // Empty type might mean an error object that wasn't fully parsed as ServerResponse
			if resp.Code != 0 {
				ps.logVerbose("Received error from server: Code %d, Message: %s", resp.Code, resp.Message)
				// Decide if this is fatal for the game loop
				if resp.Code == 400 { // Example: Bad request might mean we sent a malformed action
					// return // Potentially exit
				}
			} else {
				ps.logVerbose("Received message with empty type and no error code. Raw: %+v", resp)
			}
		default:
			// ps.logVerbose("Received game event: %s", resp.Type) // Log other events if needed
		}
	}
}

// Helper to get a pointer to an int, useful for omitempty JSON fields.
func pint(i int) *int {
	return &i
}
