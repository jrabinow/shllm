/*
shllm is a CLI REPL for talking with an LLM
*/
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chzyer/readline"
)

// AssertionFailedError represents a custom error type for failed assertions.
type AssertionFailedError struct {
	File     string
	Line     int
	Expr     string
	Expected interface{}
	Actual   interface{}
}

// Error returns the formatted error message.
func (e *AssertionFailedError) Error() string {
	return fmt.Sprintf(`%s:%d: assertion failed: %s
Expected: %v
Actual: %v
`, e.File, e.Line, e.Expr, e.Expected, e.Actual)
}

// assert checks if the condition is true, and if not, it raises an AssertionFailedError.
func assert(condition bool, format string, a ...interface{}) {
	if !condition {
		_, file, line, _ := runtime.Caller(1)
		expression := fmt.Sprintf(format, a...)
		panic(&AssertionFailedError{File: file, Line: line, Expr: expression})
	}
}

func ensureDir(dirPath string) {
	_, err := os.Stat(dirPath)
	if os.IsNotExist(err) {
		err := os.MkdirAll(dirPath, os.ModePerm)
		if err != nil {
			panic("failed to create dir")
		}
	} else if err != nil {
		panic("failed to check dir existence")
	}
}

func expandUser(path string) string {
	currentUser, err := user.Current()
	if err != nil {
		panic("failed to get current user")
	}
	if path == "~" {
		// In case of "~", which won't be caught by the "else if"
		path = currentUser.HomeDir
	} else if strings.HasPrefix(path, "~/") {
		// Use strings.HasPrefix so we don't match paths like
		// "/something/~/something/"
		path = filepath.Join(currentUser.HomeDir, path[2:])
	}
	return path
}

// https://stackoverflow.com/a/17617721
func notesDir() string {
	notesDir := os.Getenv("notes")
	return expandUser(notesDir)
}

func getSessionFilePath(filePath string) string {
	var fileName string

	if filePath != "" {
		return filePath
	}
	notesDir := notesDir()
	currentDate := time.Now().Format(time.DateOnly)

	fileName = fmt.Sprintf("%s.json", currentDate)
	archiveDir := filepath.Join(notesDir, "shllm")
	ensureDir(archiveDir)
	return filepath.Join(archiveDir, fileName)
}

func saveConversation(filePath string, convo *Conversation) {

	failWhale := func(writeData []byte, convo *Conversation) string {
		tempFile, err := ioutil.TempFile(".", "tempfile-")
		if err != nil {
			panic("the fail whale failed opening tempfile")
		}
		nbytes, err := tempFile.Write(writeData)
		if err != nil || nbytes <= 0 {
			fallbackMarshalledConvo, err := json.Marshal(*convo)
			if err != nil {
				panic("the fail whale failed marshalling data")
			}
			nbytes, err := tempFile.Write(fallbackMarshalledConvo)
			if err != nil || nbytes <= 0 {
				panic("the fail whale failed writing to tempfile")
			}
		}
		return tempFile.Name()
	}
	var convoList ConversationList

	data, err := os.ReadFile(filePath)
	if os.IsNotExist(err) || len(data) == 0 {
		data = []byte("{\"version\": 1.0}")
	} else if err != nil {
		failWhaleFileName := failWhale(nil, convo)
		panic(fmt.Sprintf("error reading data. Your conversation was saved in the current directory: %s", failWhaleFileName))
	}
	err = json.Unmarshal(data, &convoList)
	if err != nil {
		failWhaleFileName := failWhale(nil, convo)
		panic(fmt.Sprintf("error unmarshalling data. Your conversation was saved in the current directory: %s", failWhaleFileName))
	}

	convoList.Conversations = append(convoList.Conversations, *convo)
	writeData, err := json.Marshal(convoList)
	err = os.WriteFile(filePath, writeData, 0644)
	if err != nil {
		failWhaleFileName := failWhale(writeData, convo)
		panic(fmt.Sprintf("error writing file. Your conversation was saved in the current directory: %s", failWhaleFileName))
	}
}

// Message exported for use with the API definition
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Conversation exported for use with the API definition
type Conversation struct {
	Messages []Message `json:"messages"`
	Title    string    `json:"title"`
}

// ConversationList list of conversations in a file
type ConversationList struct {
	Version       float32        `json:"version"`
	Conversations []Conversation `json:"conversations"`
}

type llmResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

func llmUpdateConvo(convo Conversation) Conversation {
	defaultHeaders := map[string]string{
		"Content-Type": "application/json",
	}
	jsonBody, err := json.Marshal(convo)
	if err != nil {
		panic("couldn't marshal json")
	}
	req, err := http.NewRequest(
		"POST",
		"https://free.churchless.tech/v1/chat/completions",
		bytes.NewBuffer(jsonBody),
	)
	if err != nil {
		panic("failed to create request")
	}
	for name, headerVal := range defaultHeaders {
		req.Header.Set(name, headerVal)
	}
	ret, err := (&http.Client{}).Do(req)
	if err != nil {
		panic("network failure")
	}
	body, err := ioutil.ReadAll(ret.Body)
	if err != nil {
		panic("failure to read body")
	}
	defer ret.Body.Close()

	var jsonData llmResponse

	err = json.Unmarshal(body, &jsonData)
	if err != nil {
		panic("malformed response!")
	}
	choices := jsonData.Choices
	assert(
		len(choices) >= 0 && len(choices) <= 1,
		"len(choices) == "+strconv.Itoa(len(choices)),
	)
	choices[0].Message.Timestamp = time.Now()
	convo.Messages = append(convo.Messages, choices[0].Message)

	return convo
}

type parsedArgs struct {
	filePath string
}

func parseArgs() (parsedArgs, string) {
	flags := parsedArgs{}
	reg := regexp.MustCompile("[^a-zA-Z0-9_]+")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTIONS] [SESSION NAME]\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "Talk to chatgpt from the command line\n")
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.StringVar(&flags.filePath, "filepath", "", "filepath to save session to")
	flag.StringVar(&flags.filePath, "f", "", "filepath to use (shorthand)")
	flag.Parse()

	args := flag.Args()
	sessionTitle := strings.ToLower(reg.ReplaceAllString(strings.Join(args, "_"), ""))
	if sessionTitle == "" {
		sessionTitle = fmt.Sprintf("unnamed_session_%s", time.Now().Format(time.DateTime))
	}
	return flags, sessionTitle
}

func main() {
	flags, sessionTitle := parseArgs()
	rl, err := readline.New("\001\033[36m\002human\t  => \001\033[39m\002")
	if err != nil {
		panic(err)
	}
	defer rl.Close()
	convo := Conversation{Title: sessionTitle}

	filePath := getSessionFilePath(flags.filePath)
	defer saveConversation(filePath, &convo)

	for {
		line, err := rl.Readline()
		if err != nil {
			break
		}
		convo.Messages = append(convo.Messages, Message{Role: "user", Content: line, Timestamp: time.Now()})
		convo = llmUpdateConvo(convo)
		llmResponse := convo.Messages[len(convo.Messages)-1]

		fmt.Printf("\001\033[36m\002%s => \001\033[39m\002%s\n", llmResponse.Role, llmResponse.Content)
	}
}
