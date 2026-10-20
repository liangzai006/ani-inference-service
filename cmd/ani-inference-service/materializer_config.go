package main

import (
	"os"
	"strings"
)

type modelMaterializerSettings struct {
	Image               string
	StorageClass        string
	ServiceAccountName  string
	DownloadCAConfigMap string
	DownloadCAKey       string
}

func configuredModelMaterializerSettings() (modelMaterializerSettings, error) {
	image, err := requiredEnv("ANI_MODEL_MATERIALIZER_IMAGE")
	if err != nil {
		return modelMaterializerSettings{}, err
	}
	caKey := strings.TrimSpace(os.Getenv("ANI_MODEL_DOWNLOAD_CA_KEY"))
	if caKey == "" {
		caKey = "ca-certificates.crt"
	}
	return modelMaterializerSettings{
		Image:               image,
		StorageClass:        strings.TrimSpace(os.Getenv("ANI_MODEL_STORAGE_CLASS")),
		ServiceAccountName:  strings.TrimSpace(os.Getenv("ANI_KUBERNETES_SERVICE_ACCOUNT")),
		DownloadCAConfigMap: strings.TrimSpace(os.Getenv("ANI_MODEL_DOWNLOAD_CA_CONFIGMAP")),
		DownloadCAKey:       caKey,
	}, nil
}
