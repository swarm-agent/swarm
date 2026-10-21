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

// memoryFromMeminfo reads physical RAM once for the shared admission/memory budget.
func memoryFromMeminfo(r io.Reader) (uint64, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || kb == 0 {
				return 0, fmt.Errorf("invalid MemTotal")
			}
			if kb > ^uint64(0)/1024 {
				return 0, fmt.Errorf("MemTotal overflow")
			}
			return kb * 1024, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemTotal missing from host memory information")
}

func hostMemory() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return memoryFromMeminfo(f)
}
