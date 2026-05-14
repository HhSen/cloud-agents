package sandbox

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/l-lab/cloud-agents/internal/crypto"
	"github.com/l-lab/cloud-agents/internal/db"
)

const sshConfig = "Host *\n  StrictHostKeyChecking accept-new\n"

// maybeInjectSSHKey looks up the user's stored SSH key, decrypts it, and injects
// it into the sandbox if one is configured. No-op when the user has no SSH key.
func (m *Manager) maybeInjectSSHKey(ctx context.Context, sandboxID, username string) error {
	var user db.User
	if err := m.gormDB.Where("user_name = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("lookup user %q: %w", username, err)
	}
	if user.SSHPrivateKeyEnc == "" {
		return nil // no key stored
	}

	pem, err := crypto.Decrypt(user.SSHPrivateKeyEnc, m.sshKeySecret)
	if err != nil {
		return fmt.Errorf("decrypt SSH key for %q: %w", username, err)
	}

	if err := m.InjectSSHKey(ctx, sandboxID, []byte(pem)); err != nil {
		return err
	}
	slog.InfoContext(ctx, "SSH key injected", "sandboxID", sandboxID, "username", username)
	return nil
}

// InjectSSHKey writes the user's SSH private key into the sandbox container so that
// git operations can authenticate. Three resources are created:
//
//   - /root/.ssh/         (mode 700)
//   - /root/.ssh/id_rsa   (mode 600) — the raw PEM key
//   - /root/.ssh/config   (mode 600) — StrictHostKeyChecking accept-new
func (m *Manager) InjectSSHKey(ctx context.Context, sandboxID string, pemBytes []byte) error {
	if err := m.makeDirWithMode(ctx, sandboxID, "/root/.ssh", 700); err != nil {
		return fmt.Errorf("create .ssh dir: %w", err)
	}
	// OpenSSH requires the PEM file to end with a newline; ensure it.
	if len(pemBytes) > 0 && pemBytes[len(pemBytes)-1] != '\n' {
		pemBytes = append(pemBytes, '\n')
	}
	if err := m.writeFileWithMode(ctx, sandboxID, "/root/.ssh/id_rsa", pemBytes, 600); err != nil {
		return fmt.Errorf("write id_rsa: %w", err)
	}
	if err := m.writeFileWithMode(ctx, sandboxID, "/root/.ssh/config", []byte(sshConfig), 600); err != nil {
		return fmt.Errorf("write ssh config: %w", err)
	}
	return nil
}
