package vmaf

// AnalysisResult holds the results of VMAF analysis
type AnalysisResult struct {
	OptimalCRF  int     // CRF/CQ/QP value (0 if bitrate-based)
	QualityMod  float64 // Bitrate modifier for VideoToolbox (0 if CRF-based)
	VMafScore   float64 // Achieved VMAF score
	ShouldSkip  bool    // True if file should be skipped
	SkipReason  string  // Reason for skip
	SamplesUsed int     // Number of samples analyzed
}

// Config holds configuration for VMAF analysis
type Config struct {
	FFmpegPath     string
	TempDir        string
	SampleDuration int     // seconds
	FastAnalysis   bool    // single sample first
	VMafThreshold  float64 // target VMAF score
}
