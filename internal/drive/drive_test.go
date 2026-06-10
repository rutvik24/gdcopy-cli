package drive

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

func TestIsRetriableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "generic error",
			err:      errors.New("something went wrong"),
			expected: false,
		},
		{
			name:     "rate limit string error",
			err:      errors.New("user rate limit exceeded"),
			expected: true,
		},
		{
			name:     "quota exceeded string error",
			err:      errors.New("daily quota exceeded"),
			expected: true,
		},
		{
			name: "googleapi 403 error",
			err: &googleapi.Error{
				Code:    403,
				Message: "Rate Limit Exceeded",
			},
			expected: true,
		},
		{
			name: "googleapi 429 error",
			err: &googleapi.Error{
				Code:    429,
				Message: "Too Many Requests",
			},
			expected: true,
		},
		{
			name: "googleapi 500 error",
			err: &googleapi.Error{
				Code:    500,
				Message: "Internal Server Error",
			},
			expected: true,
		},
		{
			name: "googleapi 404 error",
			err: &googleapi.Error{
				Code:    404,
				Message: "Not Found",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := isRetriableError(tt.err)
			if actual != tt.expected {
				t.Errorf("isRetriableError(%v) = %v; expected %v", tt.err, actual, tt.expected)
			}
		})
	}
}

func TestExecuteWithRetry(t *testing.T) {
	t.Run("success first try", func(t *testing.T) {
		calls := 0
		op := func() (string, error) {
			calls++
			return "success", nil
		}
		val, err := executeWithRetry(op)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != "success" {
			t.Errorf("expected 'success', got '%s'", val)
		}
		if calls != 1 {
			t.Errorf("expected 1 call, got %d", calls)
		}
	})

	t.Run("retry and then succeed", func(t *testing.T) {
		calls := 0
		op := func() (string, error) {
			calls++
			if calls < 3 {
				return "", errors.New("rate limit exceeded")
			}
			return "success", nil
		}
		start := time.Now()
		val, err := executeWithRetry(op)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if val != "success" {
			t.Errorf("expected 'success', got '%s'", val)
		}
		if calls != 3 {
			t.Errorf("expected 3 calls, got %d", calls)
		}
		if elapsed < 2900*time.Millisecond {
			t.Errorf("expected backoff delay, got %v", elapsed)
		}
	})

	t.Run("fail after all retries", func(t *testing.T) {
		calls := 0
		op := func() (string, error) {
			calls++
			return "", errors.New("rate limit exceeded")
		}
		_, err := executeWithRetry(op)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if calls != 6 { // 1 initial try + 5 retries
			t.Errorf("expected 6 calls (1 try + 5 retries), got %d", calls)
		}
	})
}

func TestEncryptionDecryption(t *testing.T) {
	plaintext := []byte("hello world! this is a sensitive google oauth token.")
	ciphertext, err := encrypt(plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt: %v", err)
	}

	if string(ciphertext) == string(plaintext) {
		t.Fatal("ciphertext is identical to plaintext; encryption failed")
	}

	decrypted, err := decrypt(ciphertext)
	if err != nil {
		t.Fatalf("failed to decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("decrypted content does not match plaintext. Got: %s", string(decrypted))
	}
}
