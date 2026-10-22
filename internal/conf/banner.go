// banner.go renders the NovaVeil startup banner and runtime info block.
package conf

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
)

// Banner is the NovaVeil ASCII wordmark shown on startup.
const Banner = `
███╗   ██╗ ██████╗ ██╗   ██╗ █████╗ ██╗   ██╗███████╗██╗██╗ ██╗
████╗  ██║██╔═══██╗██║   ██║██╔══██╗██║   ██║██╔════╝██║██║ ██║
██╔██╗ ██║██║   ██║██║   ██║███████║██║   ██║█████╗  ██║██║ ██║
██║╚██╗██║██║   ██║╚██╗ ██╔╝██╔══██║╚██╗ ██╔╝██╔══╝  ██║██║ ██║
██║ ╚████║╚██████╔╝ ╚████╔╝ ██║  ██║ ╚████╔╝ ███████╗██║███████╗ ██║
╚═╝  ╚═══╝ ╚═════╝   ╚═══╝  ╚═╝  ╚═╝  ╚═══╝  ╚══════╝╚═╝╚══════╝ ╚═╝
`
const (
	Reset  string = "\033[0m"
	Red    string = "\033[31m"
	Green  string = "\033[32m"
	Yellow string = "\033[33m"
	Blue   string = "\033[34m"
	Purple string = "\033[35m"
	Cyan   string = "\033[36m"
	White  string = "\033[37m"
	Bold   string = "\033[1m"
	Dim    string = "\033[2m"
)

var isTerminal = func(fd uintptr) bool {
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

func printInfo(w io.Writer, label, value, printColor string, color bool) {
	if !color {
		fmt.Fprintf(w, "%-12s %s\n", label+":", value)
		return
	}
	fmt.Fprintf(w, "%s%-12s%s %s%s%s\n", Dim, label+":", Reset, printColor, value, Reset)
}

// PrintBanner writes the NovaVeil wordmark and build metadata to stdout.
func PrintBanner() {
	printBanner(os.Stdout, bannerColorEnabled(isTerminal(os.Stdout.Fd())))
}

func bannerColorEnabled(stdoutIsTTY bool) bool {
	return os.Getenv("NO_COLOR") == "" && stdoutIsTTY
}

func printBanner(w io.Writer, color bool) {
	if color {
		fmt.Fprint(w, Cyan+Bold)
	}
	fmt.Fprint(w, Banner)
	if color {
		fmt.Fprint(w, Reset)
	}

	if color {
		fmt.Fprint(w, Blue+Bold)
	}
	fmt.Fprintf(w, "          %s - %s\n", APP_NAME, APP_DESC)
	if color {
		fmt.Fprint(w, Reset)
	}

	if color {
		fmt.Fprint(w, Dim)
	}
	fmt.Fprintln(w, strings.Repeat("─", 60))
	if color {
		fmt.Fprint(w, Reset)
	}

	if IsDebug() {
		printInfo(w, "Mode", "Debug", Red, color)
	}
	printInfo(w, "Version", Version, Green, color)
	printInfo(w, "Commit", Commit[:min(8, len(Commit))], Yellow, color)
	printInfo(w, "Build Time", formatDate(BuildTime), Blue, color)
	printInfo(w, "Built By", Author, Purple, color)
	printInfo(w, "Repo", Repo, Cyan, color)

	if color {
		fmt.Fprint(w, Dim)
	}
	fmt.Fprintln(w, strings.Repeat("═", 60))
	if color {
		fmt.Fprint(w, Reset)
	}
}

func formatDate(date string) string {
	if date == "unknown" || date == "" {
		return "unknown"
	}

	layouts := []string{
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC3339,
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, date); err == nil {
			return t.Format("2006-01-02 15:04")
		}
	}

	return date
}
