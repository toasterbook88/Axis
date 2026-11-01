package transport

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const defaultPasswdFile = "/etc/passwd"

// sshPasswdFile is read directly rather than through os/user: without cgo,
// user.Current and user.LookupId fall back to $HOME when the uid has no
// passwd entry, which would make every $HOME look like the passwd home.
var sshPasswdFile = defaultPasswdFile

var (
	defaultPasswdOnce sync.Once
	defaultPasswdHome string
	defaultPasswdErr  error
)

// passwdHome returns the home field of the sshPasswdFile entry whose uid
// field is uid. A missing file, a missing entry or an empty home is an error.
func passwdHome(uid int) (string, error) {
	if sshPasswdFile == defaultPasswdFile {
		defaultPasswdOnce.Do(func() {
			defaultPasswdHome, defaultPasswdErr = readPasswdHome(defaultPasswdFile, uid)
		})
		return defaultPasswdHome, defaultPasswdErr
	}
	return readPasswdHome(sshPasswdFile, uid)
}

func readPasswdHome(path string, uid int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	want := strconv.Itoa(uid)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 7 || fields[2] != want {
			continue
		}
		if fields[5] == "" {
			return "", fmt.Errorf("%s: uid %d has an empty home", path, uid)
		}
		return fields[5], nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s: no entry for uid %d", path, uid)
}

// homeIsPasswdHome reports whether home is this uid's passwd home, in which
// case ssh would read home/.ssh/config without -F anyway.
func homeIsPasswdHome(home string) bool {
	if home == "" {
		return false
	}
	ph, err := passwdHome(os.Getuid())
	if err != nil {
		return false
	}
	a, b := filepath.Clean(home), filepath.Clean(ph)
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
