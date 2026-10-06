package config

import "testing"

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("../../config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	y, err := c.BuildYard()
	if err != nil {
		t.Fatal(err)
	}
	g, err := c.BuildGrid()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("slots=%d checkpoints=%d depth=%v", len(y.Slots()), g.Len(), g.Z)
}
