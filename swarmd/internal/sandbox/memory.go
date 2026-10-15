package sandbox

import (
	"bufio"
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var dockerMemorySize = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)([kmgtp]?)(b?)$`)

// ValidateMemory accepts an empty automatic limit or a Docker byte size of at least 1 GiB.
func ValidateMemory(value string) error {
	if value == "" {
		return nil
	}
	parts := dockerMemorySize.FindStringSubmatch(value)
	if parts != nil {
		n, ok := new(big.Rat).SetString(parts[1])
		if ok {
			shift := uint(0)
			if parts[2] != "" {
				shift = uint(strings.Index("kmgtp", strings.ToLower(parts[2]))+1) * 10
			}
			n.Mul(n, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), shift)))
			if n.Cmp(new(big.Rat).SetInt64(1<<30)) >= 0 && n.Cmp(new(big.Rat).SetInt64(1<<63-1)) <= 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("invalid sandbox memory %q: expected a Docker size of at least 1g", value)
}

// memoryFromMeminfo rounds half the physical memory down to MiB, with a 4 GiB floor.
func memoryFromMeminfo(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || kb == 0 {
				return "", fmt.Errorf("invalid MemTotal")
			}
			mib := kb / 2048
			if mib < 4096 {
				mib = 4096
			}
			if mib%1024 == 0 {
				return fmt.Sprintf("%dg", mib/1024), nil
			}
			return fmt.Sprintf("%dm", mib), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("MemTotal missing from host memory information")
}

func hostMemory() (string, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return memoryFromMeminfo(f)
}
