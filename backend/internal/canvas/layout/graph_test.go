package layout

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedFrontendLayoutFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/canvas-layout.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Cases []struct {
			Name             string
			Nodes, Obstacles []Node
			Edges            []Edge
			Expected         map[string]Position
		}
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures.Cases {
		t.Run(fixture.Name, func(t *testing.T) {
			positions := GraphWithObstacles(fixture.Nodes, fixture.Edges, fixture.Obstacles)
			if len(positions) != len(fixture.Expected) {
				t.Fatalf("unexpected node count: %v", positions)
			}
			for id, expected := range fixture.Expected {
				assertPosition(t, positions, id, expected.X, expected.Y)
			}
			second := GraphWithObstacles(withPositions(fixture.Nodes, positions), fixture.Edges, fixture.Obstacles)
			for id, expected := range positions {
				assertPosition(t, second, id, expected.X, expected.Y)
			}
			combined := append(withPositions(fixture.Nodes, positions), fixture.Obstacles...)
			if pairs := Overlaps(combined, 24); len(pairs) > 0 {
				t.Fatalf("overlapping layout: %v", pairs)
			}
		})
	}
}
