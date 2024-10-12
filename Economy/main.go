// Mercury Economy service
// "imagine a blockchain but without the blocks or the chain" - Heliodex

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	c "github.com/TwiN/go-color"
	gonanoid "github.com/matoous/go-nanoid/v2"
)

func randId() (id string) {
	id, _ = gonanoid.Generate("0123456789abcdefghijklmnopqrstuvwxyz", 15)
	return
}

func Assert(err error, txt string) {
	// so that I don't have to write this every time
	if err != nil {
		fmt.Println(c.InRed(txt+": ") + err.Error())
		os.Exit(1)
	}
}

func isDockerised() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

type (
	user     string
	currency uint64
	asset    uint64 // uint64 is overkill? idgaf
)

const (
	filepathDockerised = "./data/ledger" // jsonl file
	folderpath         = "../data/economy"
	filepath           = folderpath + "/ledger"

	Micro currency = 1
	Milli          = 1e3 * Micro
	Unit           = 1e6 * Micro // standard unit
	Kilo           = 1e3 * Unit
	Mega           = 1e6 * Unit
	Giga           = 1e9 * Unit
	Tera           = 1e12 * Unit // uint64 means ~18 tera is the economy limit (we could use math/big but that would unleash horror)

	// Target Currency per User, the economy size will try to be this * user count (len(balances))
	// By "user", I mean every user who has ever transacted with the economy
	// If I'm correct, the stipend and fee should change if the CCU is more than 10% off from this
	TCU         = float64(100 * Unit)
	baseStipend = float64(10 * Unit)
	baseFee     = 0.1
	stipendTime = 12 * 60 * 60 * 1000
)

var currentFilepath = filepath

func toReadable(c currency) string {
	return fmt.Sprintf("%d.%06d unit", c/Unit, c%Unit)
}

// For now, transaction outputs are overkill
// Since fees are stored as a separate value and are burned, I can't see a reason for them to exist for now
// UTXOs lmao
type SentTx struct {
	To         user
	From       user
	Amount     currency
	Link, Note string // Transaction links might be a bit of an ass backwards concept for now but ion care
	Returns    []asset
}
type Tx struct {
	SentTx
	Fee  currency // we ?could? store the fee as a percentage of the amount instead, unclear atm if it's worth it
	Time uint64
	Id   string
}

type SentMint struct {
	To     user
	Amount currency
	Note   string
}
type Mint struct {
	SentMint
	Time uint64
	Id   string
}

type SentBurn struct {
	From       user
	Amount     currency
	Note, Link string
	Returns    []asset
}
type Burn struct {
	SentBurn
	Time uint64
	Id   string
}

var (
	file         *os.File
	balances     = map[user]currency{}
	prevStipends = map[user]uint64{}
)

func validateTx(sent SentTx, fee currency) (e error) {
	if sent.Amount == 0 {
		e = fmt.Errorf("transaction must have an amount")
	} else if sent.From == "" {
		e = fmt.Errorf("transaction must have a sender")
	} else if sent.To == "" {
		e = fmt.Errorf("transaction must have a recipient")
	} else if sent.From == sent.To {
		e = fmt.Errorf("circular transaction: %s -> %s", sent.From, sent.To)
	} else if sent.Note == "" {
		e = fmt.Errorf("transaction must have a note")
	} else if sent.Link == "" {
		e = fmt.Errorf("transaction must have a link")
	} else if total := sent.Amount + fee; total > balances[sent.From] {
		e = fmt.Errorf("insufficient balance: balance was %s, at least %s is required", toReadable(balances[sent.From]), toReadable(total))
	}
	return
}

func validateMint(sent SentMint) (e error) {
	if sent.Amount == 0 {
		e = fmt.Errorf("mint must have an amount")
	} else if sent.To == "" {
		e = fmt.Errorf("mint must have a recipient")
	} else if sent.Note == "" {
		e = fmt.Errorf("mint must have a note")
	}
	return
}

func validateBurn(sent SentBurn) (e error) {
	if sent.Amount == 0 {
		e = fmt.Errorf("burn must have an amount")
	} else if sent.From == "" {
		e = fmt.Errorf("burn must have a sender")
	} else if sent.Amount > balances[sent.From] {
		e = fmt.Errorf("insufficient balance: balance was %s, at least %s is required", toReadable(balances[sent.From]), toReadable(sent.Amount))
	} else if sent.Note == "" {
		e = fmt.Errorf("burn must have a note")
	} else if sent.Link == "" {
		e = fmt.Errorf("burn must have a link")
	}
	return
}

func economySize() (size currency) {
	for _, v := range balances {
		size += v
	}
	return
}

// Current Currency per User
func CCU() float64 {
	users := len(balances)
	if users == 0 {
		return 0 // Division by zero causes overflowz
	}
	return float64(economySize()) / float64(users)
}

// If the economy is too small, stipends will increase
// If the economy is near or above desired size, stipends will be baseStipend
func currentStipend() currency {
	return currency(max((TCU-CCU()+baseStipend)/2, baseStipend))
}

// If the economy is too large, fees will increase
// If the economy is near or below desired size, fees will be baseFee
func currentFee() float64 {
	return max((1+(CCU()*0.9-TCU)/TCU*4)*baseFee, baseFee)
}

func handleTxTypes(lines []string, handleTx func(tx Tx), handleMint func(mint Mint), handleBurn func(burn Burn)) {
	for _, line := range lines {
		parts := strings.SplitN(line, " ", 2) // split line at first space, with the transaction type being the first part

		switch parts[0] {
		case "Transaction":
			var tx Tx
			Assert(json.Unmarshal([]byte(parts[1]), &tx), "Failed to decode transaction from ledger")

			if tx.Amount+tx.Fee > balances[tx.From] {
				fmt.Println("Invalid transaction in ledger")
				os.Exit(1)
			}

			handleTx(tx)
		case "Mint":
			var mint Mint
			Assert(json.Unmarshal([]byte(parts[1]), &mint), "Failed to decode mint from ledger")

			handleMint(mint)
		case "Burn":
			var burn Burn
			Assert(json.Unmarshal([]byte(parts[1]), &burn), "Failed to decode burn from ledger")

			if burn.Amount > balances[burn.From] {
				fmt.Println("Invalid burn in ledger")
				os.Exit(1)
			}

			handleBurn(burn)
		default:
			fmt.Println(c.InRed("Unknown transaction type in ledger"))
		}
	}
}

func loadTx(tx Tx) {
	balances[tx.From] -= tx.Amount + tx.Fee
	balances[tx.To] += tx.Amount
}

func loadMint(mint Mint) {
	balances[mint.To] += mint.Amount
	if mint.Note == "Stipend" {
		prevStipends[mint.To] = mint.Time
	}
}

func loadBurn(burn Burn) {
	balances[burn.From] -= burn.Amount
}

func updateBalances() {
	bytes, err := io.ReadAll(file)
	Assert(err, "Failed to read from ledger")

	lines := strings.Split(string(bytes), "\n")
	handleTxTypes(lines[:len(lines)-1] /* remove last empty line */, loadTx, loadMint, loadBurn)
}

func appendEvent(e any, eType string) error {
	_, err := file.WriteString(eType + " ") // Lol good luck error handling this
	if err != nil {
		println(err.Error())
	}
	return json.NewEncoder(file).Encode(e)
}

func transact(sent SentTx) error {
	fee := currency(float64(sent.Amount) * currentFee())
	if err := validateTx(sent, fee); err != nil {
		return err
	} else if err := appendEvent(
		Tx{sent, fee, uint64(time.Now().UnixMilli()), randId()},
		"Transaction",
	); err != nil {
		return err
	}

	// successfully written
	balances[sent.From] -= sent.Amount + fee
	balances[sent.To] += sent.Amount
	return nil
}

func mint(sent SentMint, time uint64) error {
	if err := validateMint(sent); err != nil {
		return err
	} else if err := appendEvent(Mint{sent, time, randId()}, "Mint"); err != nil {
		return err
	}

	// successfully written
	balances[sent.To] += sent.Amount
	return nil
}

func burn(sent SentBurn) error {
	if err := validateBurn(sent); err != nil {
		return err
	} else if err := appendEvent(Burn{sent, uint64(time.Now().UnixMilli()), randId()}, "Burn"); err != nil {
		return err
	}

	// successfully written
	balances[sent.From] -= sent.Amount
	return nil
}

func stipend(to user) error {
	time := uint64(time.Now().UnixMilli())
	if err := mint(SentMint{to, currentStipend(), "Stipend"}, time); err != nil {
		return err
	}

	prevStipends[to] = time
	return nil
}

func readTransactions() ([]string, error) {
	file, err := os.OpenFile(currentFilepath, os.O_RDONLY, 0o644)
	if err != nil {
		fmt.Println(c.InRed("Failed to open ledger:"), err)
		return []string{}, err
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		fmt.Println(c.InRed("Failed to read transactions from ledger:"), err)
		return []string{}, err
	}
	return strings.Split(string(bytes), "\n"), nil
}

func currentFeeRoute(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, currentFee())
}

func currentStipendRoute(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, currentStipend())
}

func balanceRoute(w http.ResponseWriter, r *http.Request) {
	var user user

	if _, err := fmt.Sscanf(r.PathValue("id"), "%s", &user); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	fmt.Fprint(w, balances[user])
}

func readReversed() ([]string, int, error) {
	lines, err := readTransactions()
	if err != nil {
		return lines, 0, err
	}

	for i := range len(lines) / 2 {
		j := len(lines) - i - 1
		lines[i], lines[j] = lines[j], lines[i]
	}

	return lines[1:], len(lines) - 1, nil
}

func enumerateTransactions(validate func(tx map[string]any) bool) (transactions []map[string]any, err error) {
	lines, linesLen, err := readReversed()
	if err != nil {
		return nil, err
	}

	for _, line := range lines[:min(100, linesLen)] { // Get the last 100 transactions
		parts := strings.SplitN(line, " ", 2)

		var tx any
		if err = json.Unmarshal([]byte(parts[1]), &tx); err != nil {
			return nil, err
		}

		casted := tx.(map[string]any)
		if !validate(casted) {
			continue
		}
		casted["Type"] = parts[0]
		transactions = append(transactions, casted)
	}

	return
}

func adminTransactionsRoute(w http.ResponseWriter, r *http.Request) {
	transactions, err := enumerateTransactions(func(tx map[string]any) bool {
		return true
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := json.NewEncoder(w).Encode(transactions); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func transactionsRoute(w http.ResponseWriter, r *http.Request) {
	id := user(r.PathValue("id"))

	transactions, err := enumerateTransactions(func(tx map[string]any) bool {
		return tx["From"] != nil && user(tx["From"].(string)) == id || tx["To"] != nil && user(tx["To"].(string)) == id
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := json.NewEncoder(w).Encode(transactions); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func transactRoute(w http.ResponseWriter, r *http.Request) {
	var sentTx SentTx

	if err := json.NewDecoder(r.Body).Decode(&sentTx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else if err := transact(sentTx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else {
		fmt.Println(c.InGreen(fmt.Sprintf("Transaction successful  %s -[%s]-> %s", sentTx.From, toReadable(sentTx.Amount), sentTx.To)))
	}
}

func mintRoute(w http.ResponseWriter, r *http.Request) {
	var sentMint SentMint

	if err := json.NewDecoder(r.Body).Decode(&sentMint); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else if err := mint(sentMint, uint64(time.Now().UnixMilli())); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else {
		fmt.Println(c.InGreen(fmt.Sprintf("Mint successful         %s <-[%s]-", sentMint.To, toReadable(sentMint.Amount))))
	}
}

func burnRoute(w http.ResponseWriter, r *http.Request) {
	var sentBurn SentBurn

	if err := json.NewDecoder(r.Body).Decode(&sentBurn); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else if err := burn(sentBurn); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else {
		fmt.Println(c.InGreen(fmt.Sprintf("Burn successful         %s -[%s]->", sentBurn.From, toReadable(sentBurn.Amount))))
	}
}

func stipendRoute(w http.ResponseWriter, r *http.Request) {
	var to user

	if _, err := fmt.Sscanf(r.PathValue("id"), "%s", &to); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else if prevStipends[to]+stipendTime > uint64(time.Now().UnixMilli()) {
		http.Error(w, "Next stipend not available yet", http.StatusBadRequest)
	} else if err := stipend(to); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	} else {
		fmt.Println(c.InGreen(fmt.Sprintf("Stipend successful      %s", to)))
	}
}

func main() {
	fmt.Println(c.InYellow("Loading ledger..."))
	// create the file if it dont exist
	var err error

	if isDockerised() {
		fmt.Println(c.InPurple("Running in Docker!"))
		currentFilepath = filepathDockerised
	}
	file, err = os.OpenFile(currentFilepath, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println(c.InPurple("Economy data folder not found, creating..."))
			err = os.MkdirAll(folderpath, 0o644)
			Assert(err, "Failed to create economy data folder")
			file, err = os.Create(currentFilepath)
			Assert(err, "Failed to create ledger")
		} else {
			Assert(err, "Failed to open ledger")
		}
	}
	defer file.Close()
	updateBalances()

	println("User count    ", len(balances))
	println("Economy size  ", toReadable(economySize()))
	println("CCU           ", toReadable(currency(CCU())))
	println("TCU           ", toReadable(currency(TCU)))
	println("Fee percentage", int(currentFee()*100))
	println("Stipend size  ", toReadable(currentStipend()))

	http.HandleFunc("GET /currentFee", currentFeeRoute)
	http.HandleFunc("GET /currentStipend", currentStipendRoute)
	http.HandleFunc("GET /balance/{id}", balanceRoute)
	http.HandleFunc("GET /transactions", adminTransactionsRoute)
	http.HandleFunc("GET /transactions/{id}", transactionsRoute)
	http.HandleFunc("POST /transact", transactRoute)
	http.HandleFunc("POST /mint", mintRoute)
	http.HandleFunc("POST /burn", burnRoute)
	http.HandleFunc("POST /stipend/{id}", stipendRoute)

	fmt.Println(c.InGreen("~ Economy service is up on port 2009 ~"))
	err = http.ListenAndServe(":2009", nil) // 03/Jan/2009 Chancellor on brink of second bailout for banks
	Assert(err, "Failed to start server")
}
