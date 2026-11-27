// SPDX-License-Identifier: MPL-2.0
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lucasmkrx/bugsnitch/internal/agent"
)

const modelSettingsFile = "openai.key"

type openAISettings struct {
	Key   string `json:"apiKey"`
	Model string `json:"model"`
}

func checkModelSettingsDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("The runner settings directory must be writable only by your OS user")
	}
	return nil
}

func loadOpenAISettings() (openAISettings, error) {
	var settings openAISettings
	dir, err := home()
	if err != nil {
		return settings, err
	}
	path := filepath.Join(dir, modelSettingsFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return settings, nil
	}
	if dirErr := checkModelSettingsDir(dir); dirErr != nil {
		return settings, dirErr
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return settings, errors.New("OpenAI settings require a private, regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return settings, errors.New("Could not read local OpenAI settings")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 2049))
	if err != nil || len(raw) > 2048 || json.Unmarshal(raw, &settings) != nil ||
		!agent.ValidOpenAIKey(settings.Key) || !agent.ValidOpenAIModel(settings.Model) {
		return openAISettings{}, errors.New("Invalid local OpenAI settings. Replace or remove them in Settings.")
	}
	return settings, nil
}

func saveOpenAISettings(settings openAISettings) error {
	dir, err := home()
	if err != nil {
		return err
	}
	if err := checkModelSettingsDir(dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".openai-settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(settings)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, modelSettingsFile))
}

func removeOpenAISettings() error {
	dir, err := home()
	if err != nil {
		return err
	}
	if err := checkModelSettingsDir(dir); err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, modelSettingsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func UsesOpenAIAPI() (bool, error) {
	settings, err := loadOpenAISettings()
	return settings.Key != "", err
}

func openAIOrCodex(ctx context.Context, directive, prompt, model, effort string, schema any) (agent.Result, error) {
	settings, err := loadOpenAISettings()
	if err != nil {
		return agent.Result{}, err
	}
	if settings.Key != "" {
		return agent.OpenAI(ctx, settings.Key, directive, prompt, settings.Model, schema)
	}
	c, err := agent.Open(ctx)
	if err != nil {
		return agent.Result{}, err
	}
	defer c.Close()
	return c.Turn(directive, prompt, model, effort, schema)
}
