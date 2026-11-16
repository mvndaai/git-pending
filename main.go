// Command git-pending lists git repos under a directory that have work not yet
// saved to a remote.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const usage = `usage: git-pending [filter=PATTERN] [cmd=COMMAND] [skip=stash,untracked] [DIR]
  filter  only repos whose path contains PATTERN (globs allowed)
  cmd     run COMMAND <repo> for each result, e.g. cmd=code
  skip    comma-separated checks to ignore: stash,untracked
  DIR     folder to search (default: .)
options may also be written -opt=VALUE or --opt=VALUE

reasons: not committed, untracked files, not pushed, commit(s) not pushed,
         stash(es), detached HEAD, rebase in progress, merge in progress
`

type options struct {
	filter *regexp.Regexp
	cmd    []string
	skip   map[string]bool
	root   string
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "git-pending:", err)
		os.Exit(1)
	}
	if opts == nil { // help
		fmt.Print(usage)
		return
	}

	repos := findRepos(opts.root, opts.filter)

	// Check repos concurrently, but print results in sorted order.
	results := make([][]string, len(repos))
	done := make([]chan struct{}, len(repos))
	sem := make(chan struct{}, runtime.NumCPU())
	for i, dir := range repos {
		done[i] = make(chan struct{})
		go func(i int, dir string) {
			sem <- struct{}{}
			results[i] = check(dir, opts.skip)
			<-sem
			close(done[i])
		}(i, dir)
	}

	home, _ := os.UserHomeDir()
	for i, dir := range repos {
		<-done[i]
		reasons := results[i]
		if len(reasons) == 0 {
			continue
		}
		display := dir
		if home != "" && (dir == home || strings.HasPrefix(dir, home+string(os.PathSeparator))) {
			display = "~" + dir[len(home):]
		}
		fmt.Printf("%s - %s\n", display, strings.Join(reasons, ", "))
		if len(opts.cmd) > 0 {
			c := exec.Command(opts.cmd[0], append(opts.cmd[1:], dir)...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				fmt.Fprintln(os.Stderr, "git-pending: cmd:", err)
			}
		}
	}
}

// parseArgs returns nil options (and nil error) when help was requested.
func parseArgs(args []string) (*options, error) {
	opts := &options{root: ".", skip: map[string]bool{}}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return nil, nil
		}
		key, val, hasEq := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if hasEq {
			switch key {
			case "skip":
				opts.skip = map[string]bool{}
				for s := range strings.SplitSeq(val, ",") {
					opts.skip[strings.TrimSpace(s)] = true
				}
				continue
			case "filter":
				if val == "" {
					opts.filter = nil
					continue
				}
				re, err := globToRegexp(val)
				if err != nil {
					return nil, fmt.Errorf("bad filter %q: %v", val, err)
				}
				opts.filter = re
				continue
			case "cmd":
				opts.cmd = strings.Fields(val)
				continue
			}
		}
		if strings.HasPrefix(arg, "-") {
			return nil, fmt.Errorf("unknown option %s", arg)
		}
		opts.root = arg
	}

	info, err := os.Stat(opts.root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", opts.root)
	}
	abs, err := filepath.Abs(opts.root)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	opts.root = abs
	return opts, nil
}

// globToRegexp converts a shell glob into a regexp that matches any string
// containing the glob (like zsh's *PATTERN*). Unlike filepath.Match, * and ?
// also match "/".
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") || strings.HasPrefix(class, "^") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case '\\':
			if i+1 < len(glob) {
				i++
				b.WriteString(regexp.QuoteMeta(string(glob[i])))
			} else {
				b.WriteString(`\\`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return regexp.Compile(b.String())
}

// findRepos walks root and returns the sorted list of directories containing a
// .git entry (directory or file), skipping node_modules.
func findRepos(root string, filter *regexp.Regexp) []string {
	var gitpaths []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "node_modules":
			if d.IsDir() {
				return filepath.SkipDir
			}
		case ".git":
			gitpaths = append(gitpaths, path)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.Strings(gitpaths)

	repos := make([]string, 0, len(gitpaths))
	for _, p := range gitpaths {
		dir := filepath.Dir(p)
		if filter != nil && !filter.MatchString(dir) {
			continue
		}
		repos = append(repos, dir)
	}
	return repos
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimRight(string(out), "\n"), err
}

// check returns the reasons a repo has unsaved work, or nil if it is clean.
func check(dir string, skip map[string]bool) []string {
	var reasons []string

	st, err := git(dir, "status", "--porcelain")
	if err != nil {
		return nil
	}
	var modified, untracked bool
	for line := range strings.SplitSeq(st, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "??"):
			untracked = true
		default:
			modified = true
		}
	}
	if modified {
		reasons = append(reasons, "not committed")
	}
	if untracked && !skip["untracked"] {
		reasons = append(reasons, "untracked files")
	}

	if gitdir, err := git(dir, "rev-parse", "--absolute-git-dir"); err == nil && gitdir != "" {
		if exists(filepath.Join(gitdir, "rebase-merge")) || exists(filepath.Join(gitdir, "rebase-apply")) {
			reasons = append(reasons, "rebase in progress")
		}
		if exists(filepath.Join(gitdir, "MERGE_HEAD")) {
			reasons = append(reasons, "merge in progress")
		}
	}

	branch, _ := git(dir, "symbolic-ref", "--short", "-q", "HEAD")
	upstream, _ := git(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	switch {
	case branch == "":
		reasons = append(reasons, "detached HEAD")
	case upstream == "":
		if branch != "main" && branch != "master" {
			reasons = append(reasons, fmt.Sprintf("not pushed (%s)", branch))
		}
	default:
		out, _ := git(dir, "rev-list", "--count", "@{u}..HEAD")
		if ahead, _ := strconv.Atoi(strings.TrimSpace(out)); ahead > 0 {
			reasons = append(reasons, fmt.Sprintf("%d commit(s) not pushed (%s)", ahead, branch))
		}
	}

	if !skip["stash"] {
		out, _ := git(dir, "stash", "list")
		if out != "" {
			n := strings.Count(out, "\n") + 1
			reasons = append(reasons, fmt.Sprintf("%d stash(es)", n))
		}
	}

	return reasons
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
