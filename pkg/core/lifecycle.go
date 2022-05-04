package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/LambdaTest/test-at-scale/config"
	"github.com/LambdaTest/test-at-scale/pkg/errs"
	"github.com/LambdaTest/test-at-scale/pkg/fileutils"
	"github.com/LambdaTest/test-at-scale/pkg/global"
	"github.com/LambdaTest/test-at-scale/pkg/lumber"
)

const (
	endpointPostTestResults = "http://localhost:9876/results"
	endpointPostTestList    = "http://localhost:9876/test-list"
)

// NewPipeline creates and returns a new Pipeline instance
func NewPipeline(cfg *config.NucleusConfig, logger lumber.Logger) (*Pipeline, error) {
	return &Pipeline{
		Cfg:    cfg,
		Logger: logger,
		HTTPClient: http.Client{
			Timeout: 45 * time.Second,
		},
	}, nil
}

// Start starts pipeline lifecycle
func (pl *Pipeline) Start(ctx context.Context) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	startTime := time.Now()

	pl.Logger.Debugf("Starting pipeline.....")
	pl.Logger.Debugf("Fetching config")

	// fetch configuration
	payload, err := pl.PayloadManager.FetchPayload(ctx, pl.Cfg.PayloadAddress)
	if err != nil {
		pl.Logger.Fatalf("error while fetching payload: %v", err)
	}

	err = pl.PayloadManager.ValidatePayload(ctx, payload)
	if err != nil {
		pl.Logger.Fatalf("error while validating payload %v", err)
	}

	pl.Logger.Debugf("Payload for current task: %+v \n", *payload)

	if pl.Cfg.CoverageMode {
		if err := pl.CoverageService.MergeAndUpload(ctx, payload); err != nil {
			pl.Logger.Fatalf("error while merge and upload coverage files %v", err)
		}
		os.Exit(0)
	}

	// set payload on pipeline object
	pl.Payload = payload

	taskPayload := &TaskPayload{
		TaskID:      payload.TaskID,
		BuildID:     payload.BuildID,
		RepoSlug:    payload.RepoSlug,
		RepoLink:    payload.RepoLink,
		OrgID:       payload.OrgID,
		RepoID:      payload.RepoID,
		GitProvider: payload.GitProvider,
		StartTime:   startTime,
		Status:      Running,
	}
	if pl.Cfg.DiscoverMode {
		taskPayload.Type = DiscoveryTask
	} else if pl.Cfg.FlakyMode {
		taskPayload.Type = FlakyTask
	} else {
		taskPayload.Type = ExecutionTask
	}
	payload.TaskType = taskPayload.Type
	pl.Logger.Infof("Running nucleus in %s mode", taskPayload.Type)

	// marking task to running state
	if err = pl.Task.UpdateStatus(context.Background(), taskPayload); err != nil {
		pl.Logger.Fatalf("failed to update task status %v", err)
	}

	// update task status when pipeline exits
	defer func() {
		taskPayload.EndTime = time.Now()
		if p := recover(); p != nil {
			pl.Logger.Errorf("panic stack trace: %v\n%s", p, string(debug.Stack()))
			taskPayload.Status = Error
			taskPayload.Remark = errs.GenericErrRemark.Error()
		} else if err != nil {
			if errors.Is(err, context.Canceled) {
				taskPayload.Status = Aborted
				taskPayload.Remark = "Task aborted"
			} else {
				if _, ok := err.(*errs.StatusFailed); ok {
					taskPayload.Status = Failed
				} else {
					taskPayload.Status = Error
				}
				taskPayload.Remark = err.Error()
			}
		}
		if err = pl.Task.UpdateStatus(context.Background(), taskPayload); err != nil {
			pl.Logger.Fatalf("failed to update task status %v", err)
		}
	}()

	oauth, err := pl.SecretParser.GetOauthSecret(global.OauthSecretPath)
	if err != nil {
		pl.Logger.Errorf("failed to get oauth secret %v", err)
		return err
	}
	// read secrets
	secretMap, err := pl.SecretParser.GetRepoSecret(global.RepoSecretPath)
	if err != nil {
		pl.Logger.Errorf("Error in fetching Repo secrets %v", err)
		err = errs.New(errs.GenericErrRemark.Error())
		return err
	}
	if pl.Cfg.DiscoverMode {
		pl.Logger.Infof("Cloning repo ...")
		err = pl.GitManager.Clone(ctx, pl.Payload, oauth)
		if err != nil {
			pl.Logger.Errorf("Unable to clone repo '%s': %s", payload.RepoLink, err)
			err = &errs.StatusFailed{Remark: fmt.Sprintf("Unable to clone repo: %s", payload.RepoLink)}
			return err
		}
	} else {
		pl.Logger.Debugf("Extracting workspace")
		// Replicate workspace
		if err = pl.CacheStore.ExtractWorkspace(ctx); err != nil {
			pl.Logger.Errorf("Error replicating workspace: %+v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}
	}
	coverageDir := filepath.Join(global.CodeCoverageDir, payload.OrgID, payload.RepoID, payload.BuildTargetCommit)
	if payload.CollectCoverage {
		if err = fileutils.CreateIfNotExists(coverageDir, true); err != nil {
			pl.Logger.Errorf("failed to create coverage directory %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}
	}
	// load tas yaml file
	version, err := pl.TASConfigManager.GetVersion(payload.TasFileName)
	if err != nil {
		pl.Logger.Errorf("Unable to load tas yaml file, error: %v", err)
		err = &errs.StatusFailed{Remark: err.Error()}
		return err
	}
	os.Setenv("TASK_ID", payload.TaskID)
	os.Setenv("ORG_ID", payload.OrgID)
	os.Setenv("BUILD_ID", payload.BuildID)
	// set target commit_id as environment variable
	os.Setenv("COMMIT_ID", payload.BuildTargetCommit)
	// set repo_id as environment variable
	os.Setenv("REPO_ID", payload.RepoID)
	// set coverage_dir as environment variable
	os.Setenv("CODE_COVERAGE_DIR", coverageDir)
	os.Setenv("BRANCH_NAME", payload.BranchName)
	os.Setenv("ENV", pl.Cfg.Env)
	os.Setenv("ENDPOINT_POST_TEST_LIST", endpointPostTestList)
	os.Setenv("ENDPOINT_POST_TEST_RESULTS", endpointPostTestResults)
	os.Setenv("REPO_ROOT", global.RepoDir)
	os.Setenv("BLOCK_TESTS_FILE", global.BlockTestFileLocation)
	if version >= 2 {
		// do something
		return pl.runNewVersion(ctx, payload, taskPayload, oauth, coverageDir, secretMap)
	}
	// set testing taskID, orgID and buildID as environment variable
	os.Setenv("MODULE_PATH", "")

	return pl.runOldVersion(ctx, payload, taskPayload, oauth, coverageDir, secretMap)
}

func findTaskPayloadStatus(executionResults *ExecutionResults) Status {
	for _, result := range executionResults.Results {
		for i := 0; i < len(result.TestPayload); i++ {
			testResult := &result.TestPayload[i]
			if testResult.Status == "failed" {
				return Failed
			}
		}
	}
	return Passed
}

func (pl *Pipeline) sendStats(payload ExecutionResults) error {
	endpointNeuronReport := global.NeuronHost + "/report"
	reqBody, err := json.Marshal(payload)
	if err != nil {
		pl.Logger.Errorf("failed to marshal request body %v", err)
		return err
	}

	req, err := http.NewRequest(http.MethodPost, endpointNeuronReport, bytes.NewBuffer(reqBody))
	if err != nil {
		pl.Logger.Errorf("failed to create new request %v", err)
		return err
	}

	resp, err := pl.HTTPClient.Do(req)

	if err != nil {
		pl.Logger.Errorf("error while sending reports %v", err)
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		pl.Logger.Errorf("error while sending reports, non 200 status")
		return errors.New("non 200 status")
	}
	return nil
}

func (pl *Pipeline) runOldVersion(ctx context.Context,
	payload *Payload,
	taskPayload *TaskPayload,
	oauth *Oauth,
	coverageDir string,
	secretMap map[string]string) error {
	tasConfig, err := pl.TASConfigManager.LoadAndValidateV1(ctx, payload.TasFileName, payload.EventType, payload.LicenseTier)
	if err != nil {
		pl.Logger.Errorf("Unable to load tas yaml file, error: %v", err)
		err = &errs.StatusFailed{Remark: err.Error()}
		return err
	}

	cacheKey := fmt.Sprintf("%s/%s/%s", payload.OrgID, payload.RepoID, tasConfig.Cache.Key)

	pl.Logger.Infof("Tas yaml: %+v", tasConfig)

	if tasConfig.NodeVersion != "" {
		nodeVersion := tasConfig.NodeVersion
		if nodeErr := pl.installNodeVersion(ctx, nodeVersion); nodeErr != nil {
			return nodeErr
		}
	}

	if pl.Cfg.DiscoverMode {
		blYml := pl.BlockTestService.GetBlocklistYMLV1(tasConfig)
		err = pl.BlockTestService.GetBlockTests(ctx, blYml, payload.RepoID, payload.BranchName)
		if err != nil {
			pl.Logger.Errorf("Unable to fetch blocklisted tests: %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}

		if err = pl.CacheStore.Download(ctx, cacheKey); err != nil {
			pl.Logger.Errorf("Unable to download cache: %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}

		if tasConfig.Prerun != nil {
			pl.Logger.Infof("Running pre-run steps")
			err = pl.ExecutionManager.ExecuteUserCommands(ctx, PreRun, payload, tasConfig.Prerun, secretMap, global.RepoDir)
			if err != nil {
				pl.Logger.Errorf("Unable to run pre-run steps %v", err)
				err = &errs.StatusFailed{Remark: "Failed in running pre-run steps"}
				return err
			}
		}
		err = pl.ExecutionManager.ExecuteInternalCommands(ctx, InstallRunners, global.InstallRunnerCmds, global.RepoDir, nil, nil)
		if err != nil {
			pl.Logger.Errorf("Unable to install custom runners %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}

		pl.Logger.Debugf("Caching workspace")
		// Persist workspace
		if err = pl.CacheStore.CacheWorkspace(ctx); err != nil {
			pl.Logger.Errorf("Error caching workspace: %+v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}

		pl.Logger.Infof("Identifying changed files ...")
		diffExists := true
		diff, err := pl.DiffManager.GetChangedFiles(ctx, payload, oauth)
		if err != nil {
			if errors.Is(err, errs.ErrGitDiffNotFound) {
				diffExists = false
			} else {
				pl.Logger.Errorf("Unable to identify changed files %s", err)
				err = errs.New("Error occurred in fetching diff from GitHub")
				return err
			}
		}

		// discover test cases
		err = pl.TestDiscoveryService.Discover(ctx, tasConfig, pl.Payload, secretMap, diff, diffExists)
		if err != nil {
			pl.Logger.Errorf("Unable to perform test discovery: %+v", err)
			err = &errs.StatusFailed{Remark: "Failed in discovering tests"}
			return err
		}
		// mark status as passed
		taskPayload.Status = Passed

		// Upload cache once for other builds
		if err = pl.CacheStore.Upload(ctx, cacheKey, tasConfig.Cache.Paths...); err != nil {
			pl.Logger.Errorf("Unable to upload cache: %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}
		pl.Logger.Debugf("Cache uploaded successfully")
	}

	if pl.Cfg.ExecuteMode || pl.Cfg.FlakyMode {
		// execute test cases
		executionResults, err := pl.TestExecutionService.Run(ctx, tasConfig, pl.Payload, coverageDir, secretMap)
		if err != nil {
			pl.Logger.Infof("Unable to perform test execution: %v", err)
			err = &errs.StatusFailed{Remark: "Failed in executing tests."}
			if executionResults == nil {
				return err
			}
		}

		resp, err := pl.TestExecutionService.SendResults(ctx, executionResults)
		if err != nil {
			pl.Logger.Errorf("error while sending test reports %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}
		taskPayload.Status = resp.TaskStatus

		if tasConfig.Postrun != nil {
			pl.Logger.Infof("Running post-run steps")
			err = pl.ExecutionManager.ExecuteUserCommands(ctx, PostRun, payload, tasConfig.Postrun, secretMap, global.RepoDir)
			if err != nil {
				pl.Logger.Errorf("Unable to run post-run steps %v", err)
				err = &errs.StatusFailed{Remark: "Failed in running post-run steps."}
				return err
			}
		}
	}
	pl.Logger.Debugf("Completed pipeline")

	return nil
}

func (pl *Pipeline) installNodeVersion(ctx context.Context, nodeVersion string) error {
	// Running the `source` commands in a directory where .nvmrc is present, exits with exitCode 3
	// https://github.com/nvm-sh/nvm/issues/1985
	// TODO [good-to-have]: Auto-read and install from .nvmrc file, if present
	commands := []string{
		"source /home/nucleus/.nvm/nvm.sh",
		fmt.Sprintf("nvm install %s", nodeVersion),
	}
	pl.Logger.Infof("Using user-defined node version: %v", nodeVersion)
	err := pl.ExecutionManager.ExecuteInternalCommands(ctx, InstallNodeVer, commands, "", nil, nil)
	if err != nil {
		pl.Logger.Errorf("Unable to install user-defined nodeversion %v", err)
		err = errs.New(errs.GenericErrRemark.Error())
		return err
	}
	origPath := os.Getenv("PATH")
	os.Setenv("PATH", fmt.Sprintf("/home/nucleus/.nvm/versions/node/v%s/bin:%s", nodeVersion, origPath))
	return nil
}

func (pl *Pipeline) runNewVersion(ctx context.Context,
	payload *Payload,
	taskPayload *TaskPayload,
	oauth *Oauth,
	coverageDir string,
	secretMap map[string]string) error {
	tasConfig, err := pl.TASConfigManager.LoadAndValidateV2(ctx, payload.TasFileName, payload.EventType, payload.LicenseTier)
	if err != nil {
		pl.Logger.Errorf("Unable to load tas yaml file, error: %v", err)
		err = &errs.StatusFailed{Remark: err.Error()}
		return err
	}

	if pl.Cfg.DiscoverMode {
		cacheKey := fmt.Sprintf("%s/%s/%s", payload.OrgID, payload.RepoID, tasConfig.Cache.Key)

		if err = pl.CacheStore.Download(ctx, cacheKey); err != nil {
			pl.Logger.Errorf("Unable to download cache: %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}
		/*
			Discovery steps
		*/

		pl.Logger.Infof("Identifying changed files ...")
		diffExists := true
		diff, err := pl.DiffManager.GetChangedFiles(ctx, payload, oauth)
		if err != nil {
			if errors.Is(err, errs.ErrGitDiffNotFound) {
				diffExists = false
			} else {
				pl.Logger.Errorf("Unable to identify changed files %s", err)
				err = errs.New("Error occurred in fetching diff from GitHub")
				return err
			}
		}
		wg := sync.WaitGroup{}
		if payload.EventType == EventPush {
			// iterate through all sub modules
			for i := 0; i < len(tasConfig.PostMerge.SubModules); i++ {
				//
				wg.Add(1)
				go func(subModule *SubModule) {
					if dicoveryErr := pl.runDiscoveryForEachSubModule(ctx, payload, subModule, tasConfig, secretMap,
						diff, diffExists, &wg); dicoveryErr != nil {
						pl.Logger.Errorf("error while running discovery for sub module %s, error %v", subModule.Name, dicoveryErr)
						wg.Done()
					}
				}(&tasConfig.PostMerge.SubModules[i])

			}
		} else {
			// iterate through all sub modules
			for i := 0; i < len(tasConfig.PreMerge.SubModules); i++ {
				wg.Add(1)
				go func(subModule *SubModule) {
					if dicoveryErr := pl.runDiscoveryForEachSubModule(ctx, payload, subModule, tasConfig, secretMap,
						diff, diffExists, &wg); dicoveryErr != nil {
						pl.Logger.Errorf("error while running discovery for sub module %s, error %v", subModule.Name, dicoveryErr)
						wg.Done()
					}
				}(&tasConfig.PreMerge.SubModules[i])
			}
			wg.Wait()
		}

		pl.Logger.Debugf("Caching workspace")
		// Persist workspace
		if err = pl.CacheStore.CacheWorkspace(ctx); err != nil {
			pl.Logger.Errorf("Error caching workspace: %+v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}

		// Upload cache once for other builds
		if err = pl.CacheStore.Upload(ctx, cacheKey, tasConfig.Cache.Paths...); err != nil {
			pl.Logger.Errorf("Unable to upload cache: %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}
		pl.Logger.Debugf("Cache uploaded successfully")

		// Upload cache once for other builds
		if err = pl.CacheStore.Upload(ctx, cacheKey, tasConfig.Cache.Paths...); err != nil {
			pl.Logger.Errorf("Unable to upload cache: %v", err)
			err = errs.New(errs.GenericErrRemark.Error())
			return err
		}
		pl.Logger.Debugf("Cache uploaded successfully")

	}

	pl.Logger.Infof("Tas yaml: %+v", tasConfig)
	return nil
}

func (pl *Pipeline) runDiscoveryForEachSubModule(ctx context.Context,
	payload *Payload,
	subModule *SubModule,
	tasConfig *TASConfigV2,
	secretMap map[string]string,
	diff map[string]int,
	diffExists bool,
	wg *sync.WaitGroup) error {

	blYML := pl.BlockTestService.GetBlocklistYMLV2(subModule)
	if err := pl.BlockTestService.GetBlockTests(ctx, blYML, payload.RepoID, payload.BranchName); err != nil {
		pl.Logger.Errorf("Unable to fetch blocklisted tests: %v", err)
		err = errs.New(errs.GenericErrRemark.Error())
		return err
	}
	modulePath := path.Join(global.RepoDir, subModule.Path)
	// PRE RUN steps
	if subModule.Prerun != nil {
		pl.Logger.Infof("Running pre-run steps")
		err := pl.ExecutionManager.ExecuteUserCommands(ctx, PreRun, payload, subModule.Prerun, secretMap, modulePath)
		if err != nil {
			pl.Logger.Errorf("Unable to run pre-run steps %v", err)
			err = &errs.StatusFailed{Remark: "Failed in running pre-run steps"}
			return err
		}
	}

	err := pl.ExecutionManager.ExecuteInternalCommands(ctx, InstallRunners, global.InstallRunnerCmds, modulePath, nil, nil)
	if err != nil {
		pl.Logger.Errorf("Unable to install custom runners %v", err)
		err = errs.New(errs.GenericErrRemark.Error())
		return err
	}

	err = pl.TestDiscoveryService.DiscoverV2(ctx, subModule, pl.Payload, secretMap,
		tasConfig, diff, diffExists)
	if err != nil {
		pl.Logger.Errorf("Unable to perform test discovery: %+v", err)
		err = &errs.StatusFailed{Remark: "Failed in discovering tests"}
		return err
	}
	wg.Done()
	return nil
}
