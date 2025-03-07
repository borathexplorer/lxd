package ubuntupro

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/shared/api"
)

type proCLIMock struct {
	mockResponse *api.UbuntuProGuestTokenResponse
	mockErr      error
}

func (p proCLIMock) getGuestToken(_ context.Context) (*api.UbuntuProGuestTokenResponse, error) {
	return p.mockResponse, p.mockErr
}

func TestClient(t *testing.T) {
	sleep := func() {
		time.Sleep(100 * time.Millisecond)
	}

	writeSettingsFile := func(filepath string, raw string, setting string) {
		var d []byte
		var err error
		if raw != "" {
			d = []byte(raw)
		} else {
			d, err = json.Marshal(api.UbuntuProSettings{GuestAttach: setting})
			require.NoError(t, err)
		}

		err = os.WriteFile(filepath, d, 0666)
		require.NoError(t, err)
		sleep()
	}

	mockTokenResponse := api.UbuntuProGuestTokenResponse{
		Expires:    time.Now().String(),
		GuestToken: "token",
		ID:         uuid.New().String(),
	}

	mockProCLI := proCLIMock{
		mockResponse: &mockTokenResponse,
		mockErr:      nil,
	}

	type assertion struct {
		instanceSetting   string
		expectedSetting   string
		expectErr         bool
		expectedToken     *api.UbuntuProGuestTokenResponse
		expectedErrorCode int
	}

	assertionsWhenHostHasGuestAttachmentOff := []assertion{
		{
			instanceSetting:   "",
			expectedSetting:   guestAttachSettingOff,
			expectErr:         true,
			expectedErrorCode: http.StatusForbidden,
		},
		{
			instanceSetting:   guestAttachSettingOff,
			expectedSetting:   guestAttachSettingOff,
			expectErr:         true,
			expectedErrorCode: http.StatusForbidden,
		},
		{
			instanceSetting:   guestAttachSettingAvailable,
			expectedSetting:   guestAttachSettingOff,
			expectErr:         true,
			expectedErrorCode: http.StatusForbidden,
		},
		{
			instanceSetting:   guestAttachSettingOn,
			expectedSetting:   guestAttachSettingOff,
			expectErr:         true,
			expectedErrorCode: http.StatusForbidden,
		},
	}

	assertionsWhenHostHasGuestAttachmentOnOrAvailable := []assertion{
		{
			instanceSetting:   "",
			expectedSetting:   guestAttachSettingOff,
			expectErr:         true,
			expectedErrorCode: http.StatusForbidden,
		},
		{
			instanceSetting:   guestAttachSettingOff,
			expectedSetting:   guestAttachSettingOff,
			expectErr:         true,
			expectedErrorCode: http.StatusForbidden,
		},
		{
			instanceSetting: guestAttachSettingAvailable,
			expectedSetting: guestAttachSettingAvailable,
			expectedToken:   &mockTokenResponse,
		},
		{
			instanceSetting: guestAttachSettingOn,
			expectedSetting: guestAttachSettingOn,
			expectedToken:   &mockTokenResponse,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Make a temporary directory to test file watcher behaviour.
	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)

	// Create and initialise the Client. Don't call New(), as this will create a real client watching the actual
	// /var/lib/ubuntu-advantage directory.
	s := &Client{}
	s.init(ctx, tmpDir, mockProCLI)

	runAssertions := func(assertions []assertion) {
		for _, a := range assertions {
			assert.Equal(t, api.UbuntuProSettings{GuestAttach: a.expectedSetting}, s.GuestAttachSettings(a.instanceSetting))
			token, err := s.GetGuestToken(ctx, a.instanceSetting)
			assert.Equal(t, a.expectedToken, token)
			if a.expectErr {
				assert.True(t, api.StatusErrorCheck(err, a.expectedErrorCode))
			} else {
				assert.NoError(t, err)
			}
		}
	}

	// There is no "interfaces" directory, so the guest attach setting should be off.
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	// Create the interfaces directory and sleep to wait for the filewatcher to catch up.
	interfacesDir := filepath.Join(tmpDir, "interfaces")
	err = os.Mkdir(interfacesDir, 0755)
	require.NoError(t, err)
	sleep()

	// There is no "lxd-config.json" file, so the guest attach setting should be off.
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	// Create the lxd-config.json file and sleep to wait for the filewatcher.
	lxdConfigFilepath := filepath.Join(interfacesDir, "lxd-config.json")
	f, err := os.Create(lxdConfigFilepath)
	require.NoError(t, err)
	err = f.Close()
	require.NoError(t, err)
	sleep()

	// The guest attach setting should still be false as we've not written anything to the file.
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	// Write '{"guest_attach":"available"}' to the settings file.
	writeSettingsFile(lxdConfigFilepath, "", guestAttachSettingAvailable)
	assert.Equal(t, guestAttachSettingAvailable, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOnOrAvailable)

	// Write '{"guest_attach":"off"}' to the settings file.
	writeSettingsFile(lxdConfigFilepath, "", guestAttachSettingOff)
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	// Write '{"guest_attach":"on"}' to the settings file.
	writeSettingsFile(lxdConfigFilepath, "", guestAttachSettingOn)
	assert.Equal(t, guestAttachSettingOn, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOnOrAvailable)

	// Write invalid JSON to the settings file.
	writeSettingsFile(lxdConfigFilepath, "{{}\\foo", "")
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	// Write '{"guest_attach":"on"}' to the settings file.
	writeSettingsFile(lxdConfigFilepath, "", guestAttachSettingOn)
	assert.Equal(t, guestAttachSettingOn, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOnOrAvailable)

	// Write an invalid setting to the settings file.
	writeSettingsFile(lxdConfigFilepath, "", "foo")
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	// Write '{"guest_attach":"on"}' to the settings file.
	writeSettingsFile(lxdConfigFilepath, "", guestAttachSettingOn)
	assert.Equal(t, guestAttachSettingOn, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOnOrAvailable)

	// Remove the config file.
	err = os.Remove(lxdConfigFilepath)
	require.NoError(t, err)
	sleep()
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	// Create a temporary config file and move it to the right location.
	tmpSettingsFilePath := filepath.Join(interfacesDir, "lxd-config.json.tmp")
	_, err = os.Create(tmpSettingsFilePath)
	require.NoError(t, err)
	writeSettingsFile(tmpSettingsFilePath, "", guestAttachSettingOn)
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	err = os.Rename(tmpSettingsFilePath, lxdConfigFilepath)
	require.NoError(t, err)
	sleep()
	assert.Equal(t, guestAttachSettingOn, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOnOrAvailable)

	// Cancel the context.
	cancel()
	sleep()
	assert.Equal(t, guestAttachSettingOff, s.guestAttachSetting)
	runAssertions(assertionsWhenHostHasGuestAttachmentOff)

	err = os.RemoveAll(tmpDir)
	require.NoError(t, err)
}
