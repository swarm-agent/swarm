# OpenCode Commands

This directory contains custom slash commands for OpenCode.

## Available Commands

### `/security-review`

An AI-powered security review command that analyzes code changes for vulnerabilities.

**Description:** Performs comprehensive security audit of git changes using semantic analysis

**Usage:**

```bash
/security-review
```

The command will:

1. Automatically detect git changes (staged or unstaged)
2. Analyze modified code for security vulnerabilities
3. Provide detailed findings with severity levels
4. Include remediation guidance for each issue

**What it detects:**

- **Critical**: SQL injection, command injection, authentication bypass, RCE
- **High**: XSS, CSRF, privilege escalation, sensitive data exposure
- **Medium**: Weak cryptography, insecure configuration, business logic flaws
- **Low**: Information disclosure, missing security headers

**Features:**

- Deep semantic understanding (beyond pattern matching)
- Context-aware analysis
- False positive filtering
- Actionable remediation guidance
- Severity-based prioritization

**Example output:**

```
🔴 CRITICAL: SQL Injection in User Search

Location: src/api/users.ts:42-45
Type: SQL Injection

[... detailed analysis ...]

Remediation:
Use parameterized queries to prevent SQL injection
```

### `/commit`

Git commit helper with standardized prefixes.

**Usage:**

```bash
/commit
```

### `/spellcheck`

Check spelling and grammar in markdown file changes.

**Usage:**

```bash
/spellcheck
```

### `/hello`

Example hello world command.

**Usage:**

```bash
/hello [arguments]
```

## Creating Custom Commands

To create a new command:

1. Create a new `.md` file in this directory
2. Add YAML frontmatter with a description:

   ```markdown
   ---
   description: Your command description
   ---

   Your command prompt/instructions here...
   ```

3. The filename (without `.md`) becomes the command name
4. Use it with `/your-command-name`

### Command Features

Commands support:

- `$ARGUMENTS` - Access to arguments passed to the command
- `!`command`` - Execute shell commands inline
- `@filename` - Reference specific files

For more details, see the [OpenCode documentation](https://opencode.ai/docs/commands).
