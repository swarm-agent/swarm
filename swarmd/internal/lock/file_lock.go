package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ErrAlreadyRunning = errors.New("swarmd already running")

type Metadata struct {
	PID               int    `json:"pid"`
	ProcessStartTicks uint64 `json:"process_start_ticks,omitempty"`
	Mode              string `json:"mode"`
	ListenAddr        string `json:"listen_addr"`
	StartedAt         int64  `json:"started_at"`
}

type FileLock struct {
	path string
}

var (
	currentProcessStartTicksFunc = currentProcessStartTicks
	processStartTicksFunc        = processStartTicks
	processAliveCheckFunc        = func(pid int) error { return syscall.Kill(pid, 0) }
)

func Acquire(path string, meta Metadata) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	if meta.PID > 0 && meta.ProcessStartTicks == 0 {
		startTicks, err := currentProcessStartTicksFunc()
		if err == nil {
			meta.ProcessStartTicks = startTicks
		}
	}

	for {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			enc := json.NewEncoder(f)
			if encodeErr := enc.Encode(meta); encodeErr != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("write lock metadata: %w", encodeErr)
			}
			if closeErr := f.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("close lock file: %w", closeErr)
			}
			return &FileLock{path: path}, nil
		}

		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire lock: %w", err)
		}

		stale, staleErr := staleLock(path)
		if staleErr != nil {
			return nil, staleErr
		}
		if !stale {
			return nil, ErrAlreadyRunning
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale lock: %w", removeErr)
		}
	}
}

func (l *FileLock) Release() error {
	if l == nil {
		return nil
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release lock: %w", err)
	}
	return nil
}

func staleLock(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("read lock metadata: %w", err)
	}

	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		pid, parseErr := fallbackPID(data)
		if parseErr != nil {
			return true, nil
		}
		meta.PID = pid
	}

	if meta.PID <= 0 {
		return true, nil
	}

	if meta.ProcessStartTicks > 0 {
		startTicks, err := processStartTicksFunc(meta.PID)
		switch {
		case err == nil:
			if startTicks != meta.ProcessStartTicks {
				return true, nil
			}
			return false, nil
		case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ESRCH):
			return true, nil
		}
	}

	err = processAliveCheckFunc(meta.PID)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, syscall.EPERM):
		return false, nil
	case errors.Is(err, syscall.ESRCH):
		return true, nil
	default:
		if time.Since(time.UnixMilli(meta.StartedAt)) > 24*time.Hour {
			return true, nil
		}
		return false, nil
	}
}

func currentProcessStartTicks() (uint64, error) {
	return processStartTicks(os.Getpid())
}

func processStartTicks(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("pid must be positive")
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	return parseProcessStartTicks(string(data))
}

func parseProcessStartTicks(stat string) (uint64, error) {
	stat = strings.TrimSpace(stat)
	if stat == "" {
		return 0, errors.New("empty /proc stat payload")
	}
	closing := strings.LastIndexByte(stat, ')')
	if closing < 0 || closing+1 >= len(stat) {
		return 0, errors.New("malformed /proc stat payload")
	}
	fields := strings.Fields(stat[closing+1:])
	const startTicksIndex = 19
	if len(fields) <= startTicksIndex {
		return 0, errors.New("missing process start ticks")
	}
	value, err := strconv.ParseUint(fields[startTicksIndex], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse process start ticks: %w", err)
	}
	return value, nil
}

func fallbackPID(data []byte) (int, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, errors.New("empty lock file")
	}
	pid, err := strconv.Atoi(text)
	if err != nil {
		return 0, err
	}
	return pid, nil
}
