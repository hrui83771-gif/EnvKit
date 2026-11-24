package main

import "testing"

func componentByName(c *Config, name string) *Component {
	for i := range c.Components {
		if c.Components[i].Name == name {
			return &c.Components[i]
		}
	}
	return nil
}

func TestUpgradeStaleComponentSources(t *testing.T) {
	c := &Config{Components: []Component{
		{
			Name:     "JDK",
			Version:  "17.0.12",
			URL:      staleJDK17URL,
			ZipTop:   "jdk-17.0.12+7",
			SHA256:   "old-jdk-hash",
			ExtraEnv: map[string]string{"JAVA_HOME": "<install>/jdk-17.0.12+7"},
		},
		{
			Name:    "Maven",
			Version: "3.9.9",
			URL:     staleMaven399,
			ZipTop:  "apache-maven-3.9.9",
			SHA256:  "old-maven-hash",
		},
	}}

	upgradeStaleComponentSources(c)

	jdk := componentByName(c, "JDK")
	if jdk == nil {
		t.Fatal("JDK component missing")
	}
	if jdk.Version != "17.0.21.1" || jdk.URL != jdk17URL || jdk.ZipTop != jdk17ZipTop {
		t.Fatalf("JDK not upgraded: %+v", jdk)
	}
	if jdk.SHA256 != "" {
		t.Fatalf("JDK SHA256 should be cleared, got %q", jdk.SHA256)
	}
	if got := jdk.ExtraEnv["JAVA_HOME"]; got != "<install>/jdk-17.0.21.1+2" {
		t.Fatalf("JAVA_HOME = %q", got)
	}

	mvn := componentByName(c, "Maven")
	if mvn == nil {
		t.Fatal("Maven component missing")
	}
	if mvn.Version != "3.9.16" || mvn.URL != maven316URL || mvn.ZipTop != "apache-maven-3.9.16" {
		t.Fatalf("Maven not upgraded: %+v", mvn)
	}
	if mvn.SHA256 != "" {
		t.Fatalf("Maven SHA256 should be cleared, got %q", mvn.SHA256)
	}
}

func TestUpgradeStaleComponentSourcesKeepsCustomSource(t *testing.T) {
	custom := "https://mirrors.huaweicloud.com/apache/maven/maven-3/3.9.9/binaries/apache-maven-3.9.9-bin.zip"
	c := &Config{Components: []Component{
		{Name: "Maven", Version: "3.9.9", URL: custom, ZipTop: "apache-maven-3.9.9"},
		{Name: "JDK", Version: "17.0.12", URL: "https://example.com/custom-jdk.zip", ZipTop: "jdk-custom"},
	}}

	upgradeStaleComponentSources(c)

	mvn := componentByName(c, "Maven")
	if mvn.URL != custom || mvn.Version != "3.9.9" {
		t.Fatalf("custom Maven source was modified: %+v", mvn)
	}
	jdk := componentByName(c, "JDK")
	if jdk.URL != "https://example.com/custom-jdk.zip" || jdk.Version != "17.0.12" {
		t.Fatalf("custom JDK source was modified: %+v", jdk)
	}
}
