// Package testdiscoveryservice is used for discover tests
package testdiscoveryservice

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"path"

	"github.com/LambdaTest/test-at-scale/pkg/core"
	"github.com/LambdaTest/test-at-scale/pkg/global"
	"github.com/LambdaTest/test-at-scale/pkg/logstream"
	"github.com/LambdaTest/test-at-scale/pkg/lumber"
	"github.com/LambdaTest/test-at-scale/pkg/utils"
)

type testDiscoveryService struct {
	logger      lumber.Logger
	execManager core.ExecutionManager
	tdResChan   chan core.DiscoveryResult
	requests    core.Requests
	endpoint    string
}

// NewTestDiscoveryService creates and returns a new testDiscoveryService instance
func NewTestDiscoveryService(ctx context.Context,
	tdResChan chan core.DiscoveryResult,
	execManager core.ExecutionManager,
	requests core.Requests,
	logger lumber.Logger) core.TestDiscoveryService {
	return &testDiscoveryService{
		logger:      logger,
		execManager: execManager,
		tdResChan:   tdResChan,
		requests:    requests,
		endpoint:    global.NeuronHost + "/test-list",
	}
}

func (tds *testDiscoveryService) Discover(ctx context.Context,
	tasConfig *core.TASConfig,
	payload *core.Payload,
	secretData map[string]string,
	diff map[string]int,
	diffExists bool) error {
	var target []string
	var envMap map[string]string
	if payload.EventType == core.EventPullRequest {
		target = tasConfig.Premerge.Patterns
		envMap = tasConfig.Premerge.EnvMap
	} else {
		target = tasConfig.Postmerge.Patterns
		envMap = tasConfig.Postmerge.EnvMap
	}
	tasYmlModified := false
	configFilePath, err := utils.GetConfigFileName(payload.TasFileName)
	if err != nil {
		return err
	}
	if _, ok := diff[configFilePath]; ok {
		tasYmlModified = true
	}

	// discover all tests if tas.yml modified or smart run feature is set to false
	discoverAll := tasYmlModified || !tasConfig.SmartRun

	args := []string{"--command", "discover"}
	if !discoverAll {
		if len(diff) == 0 && diffExists {
			// empty diff; in PR, a commit added and then reverted to cause an overall empty PR diff
			args = append(args, "--diff")
		} else {
			for k, v := range diff {
				// in changed files we only have added or modified files.
				if v != core.FileRemoved {
					args = append(args, "--diff", k)
				}
			}
		}
	}
	if tasConfig.ConfigFile != "" {
		args = append(args, "--config", tasConfig.ConfigFile)
	}

	for _, pattern := range target {
		args = append(args, "--pattern", pattern)
	}
	tds.logger.Debugf("Discovering tests at paths %+v", target)

	cmd := exec.CommandContext(ctx, global.FrameworkRunnerMap[tasConfig.Framework], args...)
	cmd.Dir = global.RepoDir
	envVars, err := tds.execManager.GetEnvVariables(envMap, secretData)
	if err != nil {
		tds.logger.Errorf("failed to parse env variables, error: %v", err)
		return err
	}
	cmd.Env = envVars
	logWriter := lumber.NewWriter(tds.logger)
	defer logWriter.Close()
	maskWriter := logstream.NewMasker(logWriter, secretData)
	cmd.Stdout = maskWriter
	cmd.Stderr = maskWriter

	tds.logger.Debugf("Executing test discovery command: %s", cmd.String())
	if err := cmd.Run(); err != nil {
		tds.logger.Errorf("command %s of type %s failed with error: %v", cmd.String(), core.Discovery, err)
		return err
	}

	testDiscoveryResult := <-tds.tdResChan
	testDiscoveryResult.Parallelism = tasConfig.Parallelism
	testDiscoveryResult.SplitMode = tasConfig.SplitMode
	testDiscoveryResult.ContainerImage = tasConfig.ContainerImage
	testDiscoveryResult.Tier = tasConfig.Tier
	if err := tds.updateResult(ctx, &testDiscoveryResult); err != nil {
		return err
	}
	return nil
}

func (tds *testDiscoveryService) updateResult(ctx context.Context, testDiscoveryResult *core.DiscoveryResult) error {
	tds.logger.Debugf("discover result: %+v", testDiscoveryResult)
	reqBody, err := json.Marshal(testDiscoveryResult)
	if err != nil {
		tds.logger.Errorf("error while json marshal %v", err)
		return err
	}

	if _, err := tds.requests.MakeAPIRequest(ctx, http.MethodPost, tds.endpoint, reqBody); err != nil {
		return err
	}

	return nil

}

func (tds *testDiscoveryService) DiscoverV2(ctx context.Context,
	subModule *core.SubModule,
	payload *core.Payload,
	secretData map[string]string,
	tasConfig *core.TASConfigV2,
	diff map[string]int,
	diffExists bool) error {
	var envMap map[string]string
	if payload.EventType == core.EventPullRequest {
		envMap = tasConfig.PreMerge.EnvMap
	} else {
		envMap = tasConfig.PostMerge.EnvMap
	}
	// Add submodule specific env here , overwirte the top level env specified
	for k, v := range subModule.EnvMap {
		envMap[k] = v
	}

	target := subModule.Patterns
	tasYmlModified := false
	configFilePath, err := utils.GetConfigFileName(payload.TasFileName)
	if err != nil {
		return err
	}
	if _, ok := diff[configFilePath]; ok {
		tasYmlModified = true
	}

	// discover all tests if tas.yml modified or smart run feature is set to false
	discoverAll := tasYmlModified || !tasConfig.SmartRun

	args := []string{"--command", "discover"}
	if !discoverAll {
		if len(diff) == 0 && diffExists {
			// empty diff; in PR, a commit added and then reverted to cause an overall empty PR diff
			args = append(args, "--diff")
		} else {
			for k, v := range diff {
				// in changed files we only have added or modified files.
				if v != core.FileRemoved {
					args = append(args, "--diff", k)
				}
			}
		}
	}
	if subModule.ConfigFile != "" {
		args = append(args, "--config", subModule.ConfigFile)
	}

	for _, pattern := range target {
		args = append(args, "--pattern", pattern)
	}
	tds.logger.Debugf("Discovering tests at paths %+v", target)

	cmd := exec.CommandContext(ctx, global.FrameworkRunnerMap[subModule.Framework], args...)
	cmd.Dir = path.Join(global.RepoDir, subModule.Path)
	envVars, err := tds.execManager.GetEnvVariables(envMap, secretData)
	if err != nil {
		tds.logger.Errorf("failed to parse env variables, error: %v", err)
		return err
	}
	cmd.Env = envVars
	logWriter := lumber.NewWriter(tds.logger)
	defer logWriter.Close()
	maskWriter := logstream.NewMasker(logWriter, secretData)
	cmd.Stdout = maskWriter
	cmd.Stderr = maskWriter

	tds.logger.Debugf("Executing test discovery command: %s", cmd.String())
	if err := cmd.Run(); err != nil {
		tds.logger.Errorf("command %s of type %s failed with error: %v", cmd.String(), core.Discovery, err)
		return err
	}

	testDiscoveryResult := <-tds.tdResChan
	testDiscoveryResult.Parallelism = subModule.Parallelism
	testDiscoveryResult.SplitMode = tasConfig.SplitMode
	testDiscoveryResult.SubModule = subModule.Name
	testDiscoveryResult.Tier = tasConfig.Tier
	testDiscoveryResult.ContainerImage = tasConfig.ContainerImage
	if err := tds.updateResult(ctx, &testDiscoveryResult); err != nil {
		return err
	}
	return nil
}
