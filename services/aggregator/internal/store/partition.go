package store

import "hash/fnv"

// Partitions is fixed by D-P1: 24 divides evenly across 2, 3, 4 and 6 units.
const Partitions = 24

// PartitionOf maps a hazard_id (or any other partition key) to its partition.
func PartitionOf(key string) int {
	hash := fnv.New32a()
	hash.Write([]byte(key))
	return int(hash.Sum32() % Partitions)
}
