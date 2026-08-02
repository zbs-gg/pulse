//go:build windows

package platform

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWindowsProcessInspectionUsesOneLiveInstance(t *testing.T) {
	if os.Getenv("PULSE_WINDOWS_PROCESS_HELPER") == "1" {
		fmt.Println("ready")
		time.Sleep(30 * time.Second)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=TestWindowsProcessInspectionUsesOneLiveInstance")
	command.Env = append(os.Environ(), "PULSE_WINDOWS_PROCESS_HELPER=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper readiness=%q err=%v", line, err)
	}
	identity, image, running, err := InspectWindowsProcess(command.Process.Pid)
	if err != nil || !running || identity == "" || image == "" {
		t.Fatalf("live proof identity=%q image=%q running=%v err=%v", identity, image, running, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper unexpectedly exited cleanly")
	}
	identity, image, running, err = InspectWindowsProcess(command.Process.Pid)
	if err != nil || running || identity != "" || image != "" {
		t.Fatalf("stopped proof identity=%q image=%q running=%v err=%v", identity, image, running, err)
	}
}
