package main

import (
	"log"
	"os"

	"gopkg.in/yaml.v2"
)

type conf struct {
	Database string `yaml:"database"`
	Library  string `yaml:"library"`
	Port     int    `yaml:"port"`
}

func (c *conf) getConf() *conf {

	yamlFile, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Printf("yamlFile.Get err   #%v ", err)
		c.saveDefaultConf()
		return c

	}
	err = yaml.Unmarshal(yamlFile, c)
	if err != nil {
		log.Fatalf("Unmarshal: %v", err)
	}

	return c
}
func (c *conf) saveDefaultConf() {
	c.Database = "database.sqlite"
	c.Port = 8080
	c.Library = ""
	c.saveConf()
}
func (c *conf) saveConf() {
	b, _ := yaml.Marshal(c)
	os.WriteFile("config.yaml", b, 0644)
}
