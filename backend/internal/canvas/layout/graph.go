package layout

import (
	"math"
	"sort"
)

const (
	flowRowGap     = 56.0
	laneMaxColumns = 6
	blockGap       = 160.0
	obstacleGap    = 64.0
)

type layoutRect struct{ left, top, right, bottom float64 }
type layoutBlock struct {
	positions, original map[string]Position
	width, height       float64
	anchor              Position
}

func stableNodes(nodes []Node) []Node {
	ordered := append([]Node(nil), nodes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt != ordered[j].CreatedAt {
			return ordered[i].CreatedAt < ordered[j].CreatedAt
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

func newBlock(nodes []Node) layoutBlock {
	b := layoutBlock{positions: map[string]Position{}, original: map[string]Position{}, anchor: Position{minX(nodes), minY(nodes)}}
	for _, n := range nodes {
		b.original[n.ID] = Position{n.X, n.Y}
	}
	return b
}

func mediaBlock(nodes []Node) layoutBlock {
	b := newBlock(nodes)
	top := 0.0
	for _, lane := range LaneOrder {
		members := stableNodes(laneNodes(nodes, lane))
		if len(members) == 0 {
			continue
		}
		columns := min(laneMaxColumns, max(1, ceilSqrt(len(members))))
		for start := 0; start < len(members); start += columns {
			row := members[start:min(start+columns, len(members))]
			x := 0.0
			for _, n := range row {
				b.positions[n.ID] = Position{x, top}
				x += n.width() + GridNodeGap
			}
			b.width = math.Max(b.width, x-GridNodeGap)
			top += maxHeight(row) + GridNodeGap
		}
		top += LaneGap - GridNodeGap
	}
	b.height = math.Max(0, top-LaneGap)
	return b
}

func graphBlock(nodes []Node, edges []Edge) layoutBlock {
	b := newBlock(nodes)
	byID := map[string]Node{}
	out, dagOut, dagIn := map[string][]string{}, map[string][]string{}, map[string][]string{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	for _, e := range edges {
		out[e.From] = append(out[e.From], e.To)
	}
	ordered := stableNodes(nodes)
	state := map[string]int{}
	var visit func(string)
	visit = func(id string) {
		state[id] = 1
		for _, target := range out[id] {
			if state[target] == 1 {
				continue
			}
			dagOut[id] = append(dagOut[id], target)
			dagIn[target] = append(dagIn[target], id)
			if state[target] == 0 {
				visit(target)
			}
		}
		state[id] = 2
	}
	for _, n := range ordered {
		hasInbound := false
		for _, e := range edges {
			if e.To == n.ID {
				hasInbound = true
				break
			}
		}
		if !hasInbound && state[n.ID] == 0 {
			visit(n.ID)
		}
	}
	for _, n := range ordered {
		if state[n.ID] == 0 {
			visit(n.ID)
		}
	}
	layer, pending := map[string]int{}, map[string]int{}
	queue := []string{}
	for _, n := range ordered {
		pending[n.ID] = len(dagIn[n.ID])
		if pending[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, target := range dagOut[id] {
			layer[target] = max(layer[target], layer[id]+1)
			pending[target]--
			if pending[target] == 0 {
				queue = append(queue, target)
			}
		}
	}
	layerCount := 1
	for _, value := range layer {
		layerCount = max(layerCount, value+1)
	}
	layers := make([][]string, layerCount)
	for _, n := range ordered {
		layers[layer[n.ID]] = append(layers[layer[n.ID]], n.ID)
	}
	index := map[string]int{}
	reindex := func(ids []string) {
		for i, id := range ids {
			index[id] = i
		}
	}
	for _, ids := range layers {
		reindex(ids)
	}
	sortLayer := func(ids []string, neighbors map[string][]string) {
		score := map[string]float64{}
		for _, id := range ids {
			links := neighbors[id]
			score[id] = float64(index[id])
			if len(links) > 0 {
				sum := 0.0
				for _, other := range links {
					sum += float64(index[other])
				}
				score[id] = sum / float64(len(links))
			}
		}
		sort.SliceStable(ids, func(i, j int) bool {
			if score[ids[i]] != score[ids[j]] {
				return score[ids[i]] < score[ids[j]]
			}
			return index[ids[i]] < index[ids[j]]
		})
		reindex(ids)
	}
	for sweep := 0; sweep < 4; sweep++ {
		for i := 1; i < layerCount; i++ {
			sortLayer(layers[i], dagIn)
		}
		for i := layerCount - 2; i >= 0; i-- {
			sortLayer(layers[i], dagOut)
		}
	}
	x := 0.0
	for _, ids := range layers {
		bottom, width := math.Inf(-1), 0.0
		for _, id := range ids {
			n := byID[id]
			desired := 0.0
			parents := dagIn[id]
			if len(parents) > 0 {
				for _, parent := range parents {
					desired += b.positions[parent].Y + byID[parent].height()/2
				}
				desired = desired/float64(len(parents)) - n.height()/2
			} else if !math.IsInf(bottom, -1) {
				desired = bottom + flowRowGap
			}
			y := math.Max(desired, bottom+flowRowGap)
			b.positions[id] = Position{x, y}
			bottom = y + n.height()
			width = math.Max(width, n.width())
		}
		x += width + FlowColumnGap
	}
	minY := math.Inf(1)
	for _, p := range b.positions {
		minY = math.Min(minY, p.Y)
	}
	for id, p := range b.positions {
		p.Y -= minY
		b.positions[id] = p
		b.height = math.Max(b.height, p.Y+byID[id].height())
	}
	b.width = x - FlowColumnGap
	return b
}

func rectOverlaps(a, b layoutRect, gap float64) bool {
	return a.left < b.right+gap && b.left < a.right+gap && a.top < b.bottom+gap && b.top < a.bottom+gap
}

func packBlocks(blocks []layoutBlock, origin Position, obstacles []Node) map[string]Position {
	result := map[string]Position{}
	if len(blocks) == 0 {
		return result
	}
	type occupiedRect struct {
		rect layoutRect
		gap  float64
	}
	occupied := []occupiedRect{}
	for _, n := range obstacles {
		occupied = append(occupied, occupiedRect{layoutRect{n.X, n.Y, n.X + n.width(), n.Y + n.height()}, obstacleGap})
	}
	alreadyPlaced := true
	current := []layoutRect{}
	for _, b := range blocks {
		r := layoutRect{b.anchor.X, b.anchor.Y, b.anchor.X + b.width, b.anchor.Y + b.height}
		for id, p := range b.positions {
			o := b.original[id]
			if math.Abs(o.X-b.anchor.X-p.X) >= 0.001 || math.Abs(o.Y-b.anchor.Y-p.Y) >= 0.001 {
				alreadyPlaced = false
			}
		}
		for _, fixed := range occupied {
			if rectOverlaps(r, fixed.rect, fixed.gap) {
				alreadyPlaced = false
			}
		}
		for _, other := range current {
			if rectOverlaps(r, other, blockGap) {
				alreadyPlaced = false
			}
		}
		current = append(current, r)
	}
	if alreadyPlaced {
		for _, b := range blocks {
			for id, p := range b.original {
				result[id] = p
			}
		}
		return result
	}
	totalArea, maxWidth := 0.0, 0.0
	for _, b := range blocks {
		totalArea += (b.width + blockGap) * (b.height + blockGap)
		maxWidth = math.Max(maxWidth, b.width)
	}
	rowRight := origin.X + math.Max(maxWidth, math.Sqrt(totalArea)*1.6)
	x, y, rowBottom := origin.X, origin.Y, origin.Y
	for _, b := range blocks {
		if x > origin.X && x+b.width > rowRight {
			x = origin.X
			y = rowBottom + blockGap
		}
		for {
			r := layoutRect{x, y, x + b.width, y + b.height}
			var hit *occupiedRect
			for i := range occupied {
				if rectOverlaps(r, occupied[i].rect, occupied[i].gap) {
					hit = &occupied[i]
					break
				}
			}
			if hit == nil {
				break
			}
			nextX := hit.rect.right + hit.gap
			if nextX+b.width <= rowRight {
				x = nextX
			} else {
				x = origin.X
				y = hit.rect.bottom + hit.gap
			}
		}
		for id, p := range b.positions {
			result[id] = Position{x + p.X, y + p.Y}
		}
		occupied = append(occupied, occupiedRect{layoutRect{x, y, x + b.width, y + b.height}, blockGap})
		rowBottom = math.Max(rowBottom, y+b.height)
		x += b.width + blockGap
	}
	return result
}

// GraphWithObstacles is mirrored by canvas-layout.ts; both consume the same golden fixtures.
func GraphWithObstacles(nodes []Node, edges []Edge, obstacles []Node) map[string]Position {
	if len(nodes) == 0 {
		return map[string]Position{}
	}
	ids, parent := map[string]bool{}, map[string]string{}
	for _, n := range nodes {
		ids[n.ID] = true
		parent[n.ID] = n.ID
	}
	find := func(id string) string {
		root := id
		for parent[root] != root {
			root = parent[root]
		}
		parent[id] = root
		return root
	}
	filtered := []Edge{}
	seen := map[Edge]bool{}
	for _, e := range edges {
		if e.From == e.To || !ids[e.From] || !ids[e.To] || seen[e] {
			continue
		}
		seen[e] = true
		filtered = append(filtered, e)
		parent[find(e.From)] = find(e.To)
	}
	components := map[string][]Node{}
	order := []string{}
	for _, n := range nodes {
		root := find(n.ID)
		if _, ok := components[root]; !ok {
			order = append(order, root)
		}
		components[root] = append(components[root], n)
	}
	blocks, loose := []layoutBlock{}, []Node{}
	for _, root := range order {
		members := components[root]
		if len(members) == 1 {
			loose = append(loose, members[0])
			continue
		}
		memberIDs := map[string]bool{}
		for _, n := range members {
			memberIDs[n.ID] = true
		}
		localEdges := []Edge{}
		for _, e := range filtered {
			if memberIDs[e.From] {
				localEdges = append(localEdges, e)
			}
		}
		blocks = append(blocks, graphBlock(members, localEdges))
	}
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].anchor.Y != blocks[j].anchor.Y {
			return blocks[i].anchor.Y < blocks[j].anchor.Y
		}
		return blocks[i].anchor.X < blocks[j].anchor.X
	})
	if len(loose) > 0 {
		blocks = append(blocks, mediaBlock(loose))
	}
	return packBlocks(blocks, Position{minX(nodes), minY(nodes)}, obstacles)
}
