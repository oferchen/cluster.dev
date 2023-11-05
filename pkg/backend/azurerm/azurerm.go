package azurerm

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/shalb/cluster.dev/pkg/hcltools"
	"github.com/shalb/cluster.dev/pkg/project"
	"github.com/shalb/cluster.dev/pkg/utils"
	"github.com/zclconf/go-cty/cty"
)

// Backend - describe azure backend for interface package.backend.
type Backend struct {
	name               string
	state              map[string]interface{}
	ProjectPtr         *project.Project `yaml:"-"`
	ContainerName      string           `yaml:"container_name,omitempty"`
	StorageAccountName string           `yaml:"storage_account_name,omitempty"`
	ResourceGroupName  string           `yaml:"resource_group_name,omitempty"`
	client             *azblob.Client
}

func (b *Backend) State() map[string]interface{} {
	return b.state
}

// Name return name.
func (b *Backend) Name() string {
	return b.name
}

// Provider return name.
func (b *Backend) Provider() string {
	return "azurerm"
}

// GetBackendBytes generate terraform backend config.
func (b *Backend) GetBackendBytes(stackName, unitName string) ([]byte, error) {
	f, err := b.GetBackendHCL(stackName, unitName)
	if err != nil {
		return nil, err
	}
	return f.Bytes(), nil
}

// GetBackendHCL generate terraform backend config.
func (b *Backend) GetBackendHCL(stackName, unitName string) (*hclwrite.File, error) {
	b.state["key"] = fmt.Sprintf("%s-%s.state", stackName, unitName)

	f := hclwrite.NewEmptyFile()
	rootBody := f.Body()
	terraformBlock := rootBody.AppendNewBlock("terraform", []string{})
	backendBlock := terraformBlock.Body().AppendNewBlock("backend", []string{"azurerm"})
	backendBody := backendBlock.Body()
	for key, value := range b.state {
		backendBody.SetAttributeValue(key, cty.StringVal(value.(string)))
	}
	return f, nil
}

// GetRemoteStateHCL generate terraform remote state for this backend.
func (b *Backend) GetRemoteStateHCL(stackName, unitName string) ([]byte, error) {
	b.state["key"] = fmt.Sprintf("%s-%s.state", stackName, unitName)

	f := hclwrite.NewEmptyFile()

	rootBody := f.Body()
	dataBlock := rootBody.AppendNewBlock("data", []string{"terraform_remote_state", fmt.Sprintf("%s-%s", stackName, unitName)})
	dataBody := dataBlock.Body()
	dataBody.SetAttributeValue("backend", cty.StringVal("azurerm"))
	config, err := hcltools.InterfaceToCty(b.state)
	if err != nil {
		return nil, err
	}
	dataBody.SetAttributeValue("config", config)
	return f.Bytes(), nil
}

func (b *Backend) createAzureBlobClient() error {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return err
	}

	client, err := azblob.NewClient("https://"+b.StorageAccountName+".blob.core.windows.net/", cred, nil)
	if err != nil {
		return err
	}

	b.client = client
	return nil
}

func (b *Backend) LockState() error {
	if b.client == nil {
		if err := b.createAzureBlobClient(); err != nil {
			return err
		}
	}
	lockKey := fmt.Sprintf("cdev.%s.lock", b.ProjectPtr.Name())
	ctx := context.Background()

	// Check if the blob exists.
	_, err := b.client.DownloadStream(ctx, b.ContainerName, lockKey, nil)
	if err == nil {
		return fmt.Errorf("Lock state blob found, the state is locked")
	}

	sessionID := utils.RandString(10)

	// Upload a blob to Azure Blob Storage.
	buf := []byte(sessionID)
	_, err = b.client.UploadBuffer(ctx, b.ContainerName, lockKey, buf, &azblob.UploadBufferOptions{})
	if err != nil {
		return fmt.Errorf("Can't save lock state blob: %v", err)
	}

	return nil
}

func (b *Backend) UnlockState() error {
	if b.client == nil {
		if err := b.createAzureBlobClient(); err != nil {
			return err
		}
	}

	lockKey := fmt.Sprintf("cdev.%s.lock", b.ProjectPtr.Name())
	ctx := context.Background()
	_, err := b.client.DeleteBlob(ctx, b.ContainerName, lockKey, nil)
	if err != nil {
		return fmt.Errorf("Can't unlock state: %v", err)
	}

	return nil
}

func (b *Backend) WriteState(stateData string) error {
	if b.client == nil {
		if err := b.createAzureBlobClient(); err != nil {
			return err
		}
	}

	stateKey := fmt.Sprintf("cdev.%s.state", b.ProjectPtr.Name())
	ctx := context.Background()
	buf := []byte(stateData)
	_, err := b.client.UploadBuffer(ctx, b.ContainerName, stateKey, buf, &azblob.UploadBufferOptions{})
	if err != nil {
		return fmt.Errorf("Can't save state blob: %v", err)
	}

	return nil
}

func (b *Backend) ReadState() (string, error) {
	fmt.Printf("Does not exist in container '%s'\n", b.ContainerName)
	if b.client == nil {
		if err := b.createAzureBlobClient(); err != nil {
			return "", err
		}
	}

	stateKey := fmt.Sprintf("cdev.%s.state", b.ProjectPtr.Name())
	ctx := context.Background()

	// Check if the object exists.
	_, err := b.client.DownloadStream(ctx, b.ContainerName, stateKey, nil)
	if err != nil {
		// Check if the error message contains "BlobNotFound" to identify the error.
		if strings.Contains(err.Error(), "BlobNotFound") {
			fmt.Println("The blob does not exist.")
			return "", nil
		}
		return "", err
	}

	// Download the blob
	get, err := b.client.DownloadStream(ctx, b.ContainerName, stateKey, nil)
	if err != nil {
		fmt.Errorf("Can't read state blob: %v", err)
		return "", err
	}

	stateData := bytes.Buffer{}
	retryReader := get.NewRetryReader(ctx, &azblob.RetryReaderOptions{})
	_, err = stateData.ReadFrom(retryReader)

	err = retryReader.Close()

	return stateData.String(), nil
}
