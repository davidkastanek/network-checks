package main

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/fatih/color"
	"gopkg.in/yaml.v2"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Check struct {
	Name      string        `yaml:"name"`
	CheckType string        `yaml:"type"`
	Dest      string        `yaml:"dest"`
	Repeat    time.Duration `yaml:"repeat"`
	id        int
}

type Checks struct {
	Checks []Check `yaml:"checks"`
}

func loadChecksFromYaml(path string) (Checks, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Checks{}, err
	}
	var checks Checks
	err = yaml.Unmarshal(data, &checks)
	if err != nil {
		return Checks{}, err
	}
	for i, _ := range checks.Checks {
		checks.Checks[i].id = i
	}
	return checks, nil
}

type CheckResult struct {
	check     Check
	status    bool
	runAt     time.Time
	duration  time.Duration
	execCount int
}

type CheckResultStat struct {
	last10Durations  []time.Duration
	last100Durations []time.Duration
	last50Statuses   []bool
}

func runHttpCheck(check Check, c chan CheckResult) {
	runAt := time.Now()
	resp, err := http.Get(check.Dest)
	duration := time.Since(runAt)

	checkResult := CheckResult{
		check:    check,
		runAt:    runAt,
		duration: duration,
	}

	if err != nil || resp.StatusCode != 200 {
		checkResult.status = false
	} else {
		checkResult.status = true
	}

	c <- checkResult
}

func runIcmpCheck(check Check, c chan CheckResult) {
	var cmd *exec.Cmd
	var pingOutput bytes.Buffer

	// Detect the OS and set the appropriate ping command
	if runtime.GOOS == "windows" {
		// On Windows, use -n for count and -w for timeout (in milliseconds)
		cmd = exec.Command("ping", "-n", "1", "-w", "1000", check.Dest)
	} else {
		// On Unix-like systems (Linux, macOS), use -c for count and -W for timeout (in seconds)
		cmd = exec.Command("ping", "-c", "1", "-W", "1", check.Dest)
	}

	// Capture the output
	cmd.Stdout = &pingOutput

	runAt := time.Now()
	err := cmd.Run()
	duration := time.Since(runAt)

	checkResult := CheckResult{
		check: check,
		runAt: runAt,
	}

	// Parse the ping command output to get the actual round-trip time (RTT)
	if err == nil {
		rtt, parseErr := parsePingOutput(pingOutput.String())
		if parseErr == nil {
			checkResult.duration = rtt // Set the RTT as the actual duration
			checkResult.status = true
		} else {
			checkResult.status = false
		}
	} else {
		checkResult.status = false
		checkResult.duration = duration // Fallback to the execution time if an error occurs
	}

	c <- checkResult
}

func parsePingOutput(output string) (time.Duration, error) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()

		// macOS format: round-trip min/avg/max/stddev = 8.390/8.390/8.390/0.000 ms
		if runtime.GOOS != "windows" && strings.Contains(line, "round-trip") {
			parts := strings.Split(line, "=")
			if len(parts) < 2 {
				continue // Skip this line if it doesn't have the expected format
			}
			metrics := strings.Fields(parts[1])
			if len(metrics) < 1 {
				continue // Skip if there are not enough fields
			}
			avgRTTStr := strings.Split(metrics[0], "/")[1] // Extract the average RTT
			if avgRTTStr == "" {
				continue // Ensure the average RTT string exists
			}
			rtt, err := strconv.ParseFloat(avgRTTStr, 64)
			if err != nil {
				return 0, fmt.Errorf("error parsing RTT: %v", err)
			}
			return time.Duration(rtt*1e6) * time.Nanosecond, nil // Convert ms to nanoseconds
		}

		// Windows format: Minimum = 0ms, Maximum = 0ms, Average = 0ms
		if runtime.GOOS == "windows" && strings.Contains(line, "Average =") {
			parts := strings.Split(line, "Average =")
			if len(parts) < 2 {
				continue // Skip if the line doesn't have the expected format
			}
			rttStr := strings.TrimSpace(parts[1]) // Extract the RTT value (e.g., 1ms)
			rttStr = strings.Replace(rttStr, "ms", "", 1)
			rtt, err := strconv.Atoi(rttStr)
			if err != nil {
				return 0, fmt.Errorf("error parsing Windows RTT: %v", err)
			}
			return time.Duration(rtt) * time.Millisecond, nil // Convert ms to duration
		}
	}
	return 0, fmt.Errorf("RTT not found in output")
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		// Display in milliseconds if less than 1 second
		return fmt.Sprintf("%4dms", d.Milliseconds())
	} else {
		// Display in seconds with 2 decimal places if 1 second or more
		secs := d.Seconds()
		return fmt.Sprintf("%5.2fs", secs)
	}
}

func averageDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return total / time.Duration(len(durations))
}

func prependSlice(element interface{}, slice interface{}) interface{} {
	switch s := slice.(type) {
	case []time.Duration:
		if e, ok := element.(time.Duration); ok {
			return append([]time.Duration{e}, s...)
		}
	case []bool:
		if e, ok := element.(bool); ok {
			return append([]bool{e}, s...)
		}
	}
	return slice
}

func limitSlice(slice interface{}, maxLength int) interface{} {
	switch s := slice.(type) {
	case []time.Duration:
		if len(s) > maxLength {
			return s[:maxLength]
		}
		return s
	case []bool:
		if len(s) > maxLength {
			return s[:maxLength]
		}
		return s
	default:
		return slice
	}
}

func displayResults(checkResults []CheckResult, checkResultStats []CheckResultStat, drawLock *bool) error {
	*drawLock = true
	fmt.Print("\033[H\033[2J") // Clear terminal screen
	// Print header
	fmt.Printf("%-14s %-4s   %-4s %6v | %6v | %7v | %4v | %-50s\n",
		"TARGET", "TYPE", "RES", "LAST", "LAST 10", "LAST 100", "COUNT", "HISTORY")

	for i, checkResult := range checkResults {
		statusColor := color.New(color.FgWhite)
		switch checkResult.status {
		case true:
			statusColor = color.New(color.FgGreen)
		case false:
			statusColor = color.New(color.FgRed)
		}

		statusMessage := "FAIL"
		if checkResult.status == true {
			statusMessage = "OK"
		}

		var statusHistory string
		for _, status := range checkResultStats[i].last50Statuses {
			if status {
				statusHistory += "."
			} else {
				statusHistory += "F"
			}
		}

		_, err := statusColor.Printf(
			"%-14s %-4s   %-4s %6v | %7v | %8v | %4dx | %-50s\n",
			checkResult.check.Name,
			checkResult.check.CheckType,
			statusMessage,
			formatDuration(checkResult.duration),
			formatDuration(averageDuration(checkResultStats[i].last10Durations)),
			formatDuration(averageDuration(checkResultStats[i].last100Durations)),
			checkResult.execCount,
			statusHistory,
		)
		if err != nil {
			*drawLock = true
			return err
		}
	}
	*drawLock = false
	return nil
}

func main() {
	checks, err := loadChecksFromYaml("checks.yml")
	if err != nil {
		fmt.Println("Error loading config:", err)
		os.Exit(1)
	}

	c := make(chan CheckResult)
	checkResults := make([]CheckResult, len(checks.Checks))
	checkResultStats := make([]CheckResultStat, len(checks.Checks))
	var drawLock bool = false

	for _, check := range checks.Checks {
		switch check.CheckType {
		case "http":
			go runHttpCheck(check, c)
		case "icmp":
			go runIcmpCheck(check, c)
		default:
			fmt.Println("Unknown check type:", check.CheckType)
		}
	}

	for cR := range c {
		go func(checkResult CheckResult) {
			checkResult.execCount = checkResults[checkResult.check.id].execCount + 1
			checkResults[checkResult.check.id] = checkResult

			checkResultStats[checkResult.check.id].last10Durations = limitSlice(prependSlice(checkResult.duration, checkResultStats[checkResult.check.id].last10Durations).([]time.Duration), 10).([]time.Duration)
			checkResultStats[checkResult.check.id].last100Durations = limitSlice(prependSlice(checkResult.duration, checkResultStats[checkResult.check.id].last100Durations).([]time.Duration), 100).([]time.Duration)
			checkResultStats[checkResult.check.id].last50Statuses = limitSlice(prependSlice(checkResult.status, checkResultStats[checkResult.check.id].last50Statuses).([]bool), 50).([]bool)

			if drawLock == false {
				displayResults(checkResults, checkResultStats, &drawLock)
			}

			time.Sleep(checkResult.check.Repeat)
			switch checkResult.check.CheckType {
			case "http":
				runHttpCheck(checkResult.check, c)
			case "icmp":
				go runIcmpCheck(checkResult.check, c)
			default:
				fmt.Println("Unknown check type:", checkResult.check.CheckType)
			}
		}(cR)
	}
}
