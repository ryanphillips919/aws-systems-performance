package main

import (
	"math"
	"reflect"
	"testing"
)

func TestSimulation(t *testing.T) {
	for _, p := range []float64{0, 1} {
		for _, point := range simulate(p, 100, 1) {
			if point.Theoretical != p || point.Simulated != p {
				t.Fatalf("boundary p=%v: %+v", p, point)
			}
		}
	}
	a, b := simulate(.99, 100000, 42), simulate(.99, 100000, 42)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("simulation is not reproducible")
	}
	for _, point := range a {
		if math.Abs(point.Theoretical-point.Simulated) > .01 {
			t.Fatalf("unexpected simulation result %+v", point)
		}
	}
}
