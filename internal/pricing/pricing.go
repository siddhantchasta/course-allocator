package pricing

const (
	// BaseCredits is charged while seats are still plentiful.
	BaseCredits = 10
	// MaxCredits caps the surge price so it can never run away.
	MaxCredits = 20
	// TotalSeats is the course capacity used to compute scarcity.
	TotalSeats = 100
	// surgeThreshold is the seat count at/above which no surge applies.
	surgeThreshold = TotalSeats / 2
	// seatsPerCreditStep controls how quickly price climbs as seats run out:
	// every this-many seats sold past the threshold adds 1 credit.
	seatsPerCreditStep = 10
)

// Calculate returns the credit cost for a registration given the number of
// seats currently available. Price stays at BaseCredits until half the
// course is full, then climbs in step with scarcity up to MaxCredits.
func Calculate(availableSeats int) int {
	if availableSeats <= 0 {
		return MaxCredits
	}
	if availableSeats >= surgeThreshold {
		return BaseCredits
	}

	seatsSoldPastThreshold := surgeThreshold - availableSeats
	surge := seatsSoldPastThreshold / seatsPerCreditStep
	credits := BaseCredits + surge

	if credits > MaxCredits {
		return MaxCredits
	}
	return credits
}
