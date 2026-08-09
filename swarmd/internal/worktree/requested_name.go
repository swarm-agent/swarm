package worktree

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"
)

const (
	maxRequestedWorktreeBranchNameBytes   = 255
	requestedWorktreeRetryIdentifierMin   = int64(10000)
	requestedWorktreeRetryIdentifierRange = int64(90000)
)

// CanonicalizeRequestedWorktreeName converts a Router-supplied worktree name
// into the exact branch name used for allocation. The configured branch name
// may be either a prefix (for example, "agent") or the existing <id> template
// form (for example, "agent/<id>").
//
// Router names are untrusted. This function deliberately rejects Git ref and
// path constructs rather than attempting to repair them. Within a valid name,
// letter case is normalized and human-readable separators are collapsed to a
// single hyphen.
func CanonicalizeRequestedWorktreeName(requestedName, configuredBranchName string) (string, error) {
	prefix, err := requestedWorktreeBranchPrefix(configuredBranchName)
	if err != nil {
		return "", err
	}
	slug, err := requestedWorktreeNameSlug(requestedName)
	if err != nil {
		return "", err
	}
	branchName := prefix + "/" + slug
	if len(branchName) > maxRequestedWorktreeBranchNameBytes {
		return "", fmt.Errorf("requested worktree branch name exceeds %d bytes", maxRequestedWorktreeBranchNameBytes)
	}
	if err := validateRequestedWorktreeRef(branchName); err != nil {
		return "", fmt.Errorf("requested worktree branch name: %w", err)
	}
	return branchName, nil
}

// CanonicalizeRequestedWorktreeNameRetry returns the sole duplicate-name retry
// candidate. A random five-digit identifier is appended to the original Router
// name before the same canonical validation and prefix application as the first
// allocation attempt.
func CanonicalizeRequestedWorktreeNameRetry(requestedName, configuredBranchName string) (branchName, retryRequestedName string, err error) {
	identifier, err := rand.Int(rand.Reader, big.NewInt(requestedWorktreeRetryIdentifierRange))
	if err != nil {
		return "", "", fmt.Errorf("generate requested worktree retry identifier: %w", err)
	}
	retryRequestedName = fmt.Sprintf("%s %05d", strings.TrimSpace(requestedName), identifier.Int64()+requestedWorktreeRetryIdentifierMin)
	branchName, err = CanonicalizeRequestedWorktreeName(retryRequestedName, configuredBranchName)
	if err != nil {
		return "", "", err
	}
	return branchName, retryRequestedName, nil
}

func requestedWorktreeBranchPrefix(configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return defaultWorktreeBranchPrefix, nil
	}

	trimmed := strings.Trim(configured, "/")
	if trimmed == "" {
		return "", errors.New("worktree branch prefix is invalid")
	}
	if strings.EqualFold(trimmed, defaultWorktreeBranchName) || strings.EqualFold(trimmed, worktreeBranchIDPlaceholder) {
		trimmed = defaultWorktreeBranchPrefix
	} else if strings.HasSuffix(trimmed, "/"+worktreeBranchIDPlaceholder) {
		trimmed = strings.TrimSuffix(trimmed, "/"+worktreeBranchIDPlaceholder)
		trimmed = strings.Trim(trimmed, "/")
	}
	if trimmed == "" {
		return "", errors.New("worktree branch prefix is invalid")
	}
	if err := validateRequestedWorktreeRef(trimmed); err != nil {
		return "", fmt.Errorf("worktree branch prefix: %w", err)
	}
	return trimmed, nil
}

func requestedWorktreeNameSlug(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("requested worktree name is required")
	}
	if strings.HasPrefix(requested, "-") || strings.HasPrefix(requested, ".") || strings.HasSuffix(requested, ".") || strings.HasSuffix(strings.ToLower(requested), ".lock") {
		return "", errors.New("requested worktree name contains an invalid Git ref component")
	}
	if strings.ContainsAny(requested, `/\\`) || strings.Contains(requested, "..") || strings.Contains(requested, "@{") {
		return "", errors.New("requested worktree name contains a Git ref or path construct")
	}

	var slug strings.Builder
	separator := false
	for _, r := range requested {
		switch {
		case r >= 'A' && r <= 'Z':
			if separator && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			separator = false
			slug.WriteRune(r + ('a' - 'A'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if separator && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			separator = false
			slug.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == '(' || r == ')' || unicode.IsSpace(r):
			separator = slug.Len() > 0
		default:
			return "", fmt.Errorf("requested worktree name contains unsupported character %q", r)
		}
	}
	canonical := strings.Trim(slug.String(), "-")
	if canonical == "" {
		return "", errors.New("requested worktree name must contain letters or digits")
	}
	return canonical, nil
}

func validateRequestedWorktreeRef(ref string) error {
	if ref == "" {
		return errors.New("Git ref is empty")
	}
	if ref == "@" || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.Contains(ref, "//") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") || strings.HasSuffix(ref, ".") {
		return errors.New("invalid Git ref path")
	}
	if strings.ContainsAny(ref, " ~^:?*[\\") {
		return errors.New("invalid Git ref character")
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return errors.New("invalid Git ref control character")
		}
	}
	for _, component := range strings.Split(ref, "/") {
		lower := strings.ToLower(component)
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") || strings.HasSuffix(lower, ".lock") {
			return errors.New("invalid Git ref component")
		}
	}
	return nil
}
