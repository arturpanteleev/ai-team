package config

type Config struct {
	Pipeline []string `yaml:"pipeline"`
	CLI      string   `yaml:"cli"`
	Model    string   `yaml:"model"`
}
