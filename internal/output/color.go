package output

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

var (
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	gray   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

func ColorStatus(status string) string {
	if noColor() {
		return status
	}
	switch status {
	case "SUCCESS":
		return green.Render(status)
	case "FAILURE":
		return red.Render(status)
	case "UNSTABLE":
		return yellow.Render(status)
	case "ABORTED", "NOT_BUILT", "DISABLED":
		return gray.Render(status)
	case "BUILDING":
		return cyan.Render(status)
	default:
		return status
	}
}

func noColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeCharDevice == 0
}
