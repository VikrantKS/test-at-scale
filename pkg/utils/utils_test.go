package utils

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/LambdaTest/test-at-scale/pkg/core"
	"github.com/LambdaTest/test-at-scale/pkg/errs"
	"github.com/LambdaTest/test-at-scale/testutils"
	"github.com/stretchr/testify/assert"
)

const (
	directory = "../../testutils/testdirectory"
)

func TestMin(t *testing.T) {
	type args struct {
		x int
		y int
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{"x: 5, y: -1", args{5, -1}, -1},
		{"x: 0, y: 0", args{0, 0}, 0},
		{"x: -293836, y: 0", args{-293836, 0}, -293836},
		{"x: 2545, y: 374", args{2545, 374}, 374},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Min(tt.args.x, tt.args.y); got != tt.want {
				t.Errorf("Min() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestComputeChecksum(t *testing.T) {
	_, err := os.Create("dummy_file")
	if err != nil {
		fmt.Printf("Error in creating file, error: %v", err)
	}
	type args struct {
		filename string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{"dummy_file_name", args{"dummy_file_name"}, "", true},
		{"dummy_file", args{"dummy_file"}, "d41d8cd98f00b204e9800998ecf8427e", false},
	}
	defer removeCreatedFile("dummy_file")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ComputeChecksum(tt.args.filename)
			if (err != nil) != tt.wantErr {
				t.Errorf("ComputeChecksum() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ComputeChecksum() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateDirectory(t *testing.T) {
	newDir := "../../testutils/nonExistingDir"
	existDir := directory
	type args struct {
		path string
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{"Existing directory: ../../testutils/testdirecotry", args{existDir}, false},
		{"Non-existing directory: ../../testutils/nonExistingDir", args{newDir}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CreateDirectory(tt.args.path); (err != nil) != tt.wantErr {
				t.Errorf("CreateDirectory() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.args.path == newDir {
				if _, err := os.Lstat(newDir); err != nil {
					t.Errorf("Directory did not exist, error: %v", err)
					return
				}
				defer removeCreatedFile(newDir)
			}
		})
	}
}

func TestWriteFileToDirectory(t *testing.T) {
	path := directory
	filename := "writeFileToDirectory"
	data := []byte("Hello world!")
	err := WriteFileToDirectory(path, filename, data)
	if err != nil {
		t.Errorf("Error: %v", err)
		return
	}
	defer removeCreatedFile(filepath.Join(path, filename))
	checkData, err := os.ReadFile(filepath.Join(path, filename))
	if err != nil {
		t.Errorf("Error: %v", err)
		return
	}
	if string(checkData) != "Hello world!" {
		t.Errorf("expected file contents: Hello world!, got: %s", string(checkData))
	}
}

func TestGetOutboundIP(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Test1", "http://synapse:8000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetOutboundIP(); got != tt.want {
				t.Errorf("GetOutboundIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateStructv1(t *testing.T) {
	ctx := context.TODO()
	tests := []struct {
		name     string
		filename string
		wantErr  error
		want     *core.TASConfig
	}{
		{
			"Junk characters File",
			"testutils/testdata/tasyml/junk.yml",
			// nolint:lll
			fmt.Errorf("`testutils/testdata/tasyml/junk.yml` configuration file contains invalid format. Please correct the `testutils/testdata/tasyml/junk.yml` file"),
			nil,
		},
		{
			"Invalid Types",
			"testutils/testdata/tasyml/invalid_types.yml",
			// nolint:lll
			fmt.Errorf("`testutils/testdata/tasyml/invalid_types.yml` configuration file contains invalid format. Please correct the `testutils/testdata/tasyml/invalid_types.yml` file"),
			nil,
		},
		{
			"Invalid Field Values",
			"testutils/testdata/tasyml/invalid_fields.yml",
			errs.ErrInvalidConf{
				// nolint:lll
				Message: "Invalid values provided for the following fields in the `testutils/testdata/tasyml/invalid_fields.yml` configuration file: \n",
				Fields:  []string{"framework", "nodeVersion"},
				Values:  []interface{}{"hello", "test"}},
			nil,
		},
		{
			"Valid Config",
			"testutils/testdata/tasyml/valid.yml",
			nil,
			&core.TASConfig{
				SmartRun:  true,
				Framework: "jest",
				Postmerge: &core.Merge{
					EnvMap:   map[string]string{"NODE_ENV": "development"},
					Patterns: []string{"{packages,scripts}/**/__tests__/*{.js,.coffee,[!d].ts}"},
				},
				Premerge: &core.Merge{
					EnvMap:   map[string]string{"NODE_ENV": "development"},
					Patterns: []string{"{packages,scripts}/**/__tests__/*{.js,.coffee,[!d].ts}"},
				},
				Prerun:      &core.Run{EnvMap: map[string]string{"NODE_ENV": "development"}, Commands: []string{"yarn"}},
				Postrun:     &core.Run{Commands: []string{"node --version"}},
				ConfigFile:  "scripts/jest/config.source-www.js",
				NodeVersion: "14.17.6",
				Tier:        "small",
				SplitMode:   core.TestSplit,
				Version:     "1.0",
			},
		},
		{
			"Valid Config - Only Framework",
			"testutils/testdata/tasyml/framework_only_required.yml",
			nil,
			&core.TASConfig{
				SmartRun:  true,
				Framework: "mocha",
				Tier:      "small",
				SplitMode: core.TestSplit,
				Version:   "1.2",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ymlContent, err := testutils.LoadFile(tt.filename)
			if err != nil {
				t.Errorf("Error loading testfile %s", tt.filename)
				return
			}
			tasConfig, errV := ValidateStructTASYmlV1(ctx, ymlContent, tt.filename)
			if errV != nil {
				assert.Equal(t, errV.Error(), tt.wantErr.Error(), "Error mismatch")
				return
			}
			assert.Equal(t, tt.want, tasConfig, "Struct mismatch")
		})
	}
}

func removeCreatedFile(path string) {
	err := os.RemoveAll(path)
	if err != nil {
		fmt.Println("error in removing!!")
	}
}
func TestValidateStructv2(t *testing.T) {
	ctx := context.TODO()
	tests := []struct {
		name     string
		filename string
		wantErr  error
		want     *core.TASConfigV2
	}{
		{
			"Junk characters File",
			"testutils/testdata/tasyml/junk.yml",
			// nolint:lll
			fmt.Errorf("`testutils/testdata/tasyml/junk.yml` configuration file contains invalid format. Please correct the `testutils/testdata/tasyml/junk.yml` file"),
			nil,
		},
		{
			"Invalid Types",
			"testutils/testdata/tasyml/invalid_typesv2.yml",
			// nolint:lll
			fmt.Errorf("`testutils/testdata/tasyml/invalid_typesv2.yml` configuration file contains invalid format. Please correct the `testutils/testdata/tasyml/invalid_typesv2.yml` file"),
			nil,
		},

		{
			"Valid Config",
			"testutils/testdata/tasyml/validV2.yml",
			nil,
			&core.TASConfigV2{
				SmartRun:  true,
				Tier:      "small",
				SplitMode: core.TestSplit,
				PostMerge: core.Mergev2{
					SubModules: []core.SubModule{
						{
							Name: "some-module-1",
							Path: "./somepath",
							Patterns: []string{
								"./x/y/z",
							},
							Framework:   "mocha",
							NodeVersion: "17.0.1",
							ConfigFile:  "x/y/z",
						},
					},
				},
				PreMerge: core.Mergev2{
					SubModules: []core.SubModule{
						{
							Name: "some-module-1",
							Path: "./somepath",
							Patterns: []string{
								"./x/y/z",
							},
							Framework:   "jasmine",
							NodeVersion: "17.0.1",
							ConfigFile:  "/x/y/z",
						},
					},
				},
				Parallelism: 1,
				Version:     "2.0.1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ymlContent, err := testutils.LoadFile(tt.filename)
			if err != nil {
				t.Errorf("Error loading testfile %s", tt.filename)
				return
			}
			tasConfig, errV := ValidateStructTASYmlV2(ctx, ymlContent, tt.filename)
			if errV != nil {
				assert.Equal(t, errV.Error(), tt.wantErr.Error(), "Error mismatch")
				return
			}

			assert.Equal(t, tt.want, tasConfig, "Struct mismatch")
		})
	}
}

func TestGetVersion(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantErr  error
		want     int
	}{
		{
			"Test with invalid version type",
			"testutils/testdata/tasyml/invalidVersion.yml",
			fmt.Errorf("strconv.Atoi: parsing \"a\": invalid syntax"),
			0,
		},
		{
			"Test valid yml type for tas version 1",
			"testutils/testdata/tasyml/valid.yml",
			nil,
			1,
		},
		{
			"Test valid yml type for tas version 2",
			"testutils/testdata/tasyml/validV2.yml",
			nil,
			2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ymlContent, err := testutils.LoadFile(tt.filename)
			if err != nil {
				t.Errorf("Error loading testfile %s", tt.filename)
				return
			}
			version, errV := GetVersion(ymlContent)
			if errV != nil {
				assert.Equal(t, errV.Error(), tt.wantErr.Error(), "Error mismatch")
				return
			}
			assert.Equal(t, tt.want, version, "value mismatch")
		})
	}
}

func TestValidateSubModule(t *testing.T) {
	tests := []struct {
		name      string
		subModule core.SubModule
		wantErr   error
	}{
		{
			"Test submodule if name is empty",
			core.SubModule{
				Path:     "/x/y",
				Patterns: []string{"/a/c"},
			},

			errs.New("module name is not defined"),
		},
		{
			"Test submodule if path is empty",
			core.SubModule{
				Name:     "some name",
				Patterns: []string{"/a/c"},
			},

			errs.New("module path is not defined for module some name "),
		},
		{
			"Test submodule if pattern length is empty",
			core.SubModule{
				Name: "some-name",
				Path: "/x/y",
			},

			errs.New("module some-name pattern length is 0"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotErr := ValidateSubModule(&tt.subModule)
			assert.Equal(t, tt.wantErr, gotErr, "Error mismatch")
		})
	}
}
