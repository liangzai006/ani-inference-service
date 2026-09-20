package main

type modelMaterializerSettings struct {
	Image        string
	StorageClass string
}

func configuredModelMaterializerSettings() (modelMaterializerSettings, error) {
	image, err := requiredEnv("ANI_MODEL_MATERIALIZER_IMAGE")
	if err != nil {
		return modelMaterializerSettings{}, err
	}
	return modelMaterializerSettings{
		Image:        image,
		StorageClass: envOrDefault("ANI_MODEL_STORAGE_CLASS", "cephfs"),
	}, nil
}
