package satellite

import "time"

type detectionGrid struct {
	points map[string]point
	cells  map[[4]int64]map[string]point
	active map[string]string
}

func (g detectionGrid) add(key string, p point) {
	g.points[key] = p
	c := cell(p)
	if g.cells[c] == nil {
		g.cells[c] = make(map[string]point)
	}
	g.cells[c][key] = p
}

func (g detectionGrid) remove(key string, p point) {
	delete(g.points, key)
	c := cell(p)
	delete(g.cells[c], key)
	if len(g.cells[c]) == 0 {
		delete(g.cells, c)
	}
}

func (g detectionGrid) neighbors(key string, p point, visit func(string)) {
	c := cell(p)
	for x := c[0] - 1; x <= c[0]+1; x++ {
		for y := c[1] - 1; y <= c[1]+1; y++ {
			for z := c[2] - 1; z <= c[2]+1; z++ {
				for t := c[3] - 1; t <= c[3]+1; t++ {
					for otherKey, other := range g.cells[[4]int64{x, y, z, t}] {
						dt := p.at.Sub(other.at)
						if otherKey != key && dt >= -6*time.Hour && dt <= 6*time.Hour && distance(p, other) <= 1 {
							visit(otherKey)
						}
					}
				}
			}
		}
	}
}
