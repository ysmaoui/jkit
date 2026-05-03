package cmd

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ysmaoui/jk/internal/api"
	appctx "github.com/ysmaoui/jk/internal/context"
)

var openCmd = &cobra.Command{
	Use:   "open [job] [build#]",
	Short: "Open Jenkins in browser",
	Example: `  jk open
  jk open my-app
  jk open my-app 42`,
	Args: cobra.MaximumNArgs(2),
	RunE: runOpen,
}

func init() {
	rootCmd.AddCommand(openCmd)
}

func runOpen(cmd *cobra.Command, args []string) error {
	// If given a full URL, just open it directly
	if len(args) > 0 && (strings.HasPrefix(args[0], "http://") || strings.HasPrefix(args[0], "https://")) {
		fmt.Printf("Opening %s\n", args[0])
		return openBrowser(args[0])
	}

	host, err := hostFromCmd(cmd)
	if err != nil {
		return err
	}

	var jobPath string
	var buildNumArg string
	if len(args) >= 1 {
		jobPath = args[0]
	}
	if len(args) == 2 {
		buildNumArg = args[1]
	}
	if jobPath == "" {
		resolved, err := appctx.Resolve()
		if err != nil {
			return err
		}
		jobPath = resolved.JobPath
	}

	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		return fmt.Errorf("host URL must start with http:// or https://")
	}

	u := host + api.NormalizeJobPath(jobPath)

	if buildNumArg != "" {
		num, err := strconv.Atoi(buildNumArg)
		if err != nil {
			return fmt.Errorf("invalid build number: %s", buildNumArg)
		}
		u = fmt.Sprintf("%s/%d", u, num)
	}

	fmt.Printf("Opening %s\n", u)
	return openBrowser(u)
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return fmt.Errorf("unsupported platform %s", runtime.GOOS)
	}
}
