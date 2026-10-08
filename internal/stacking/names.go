package stacking

const (
	frameTypeLight = "LIGHT"
	frameTypeDark  = "DARK"
)

const (
	filterRed   = "Red"
	filterGreen = "Green"
	filterBlue  = "Blue"
	filterHa    = "H-a"
	filterOIII  = "O-III"
	filterSII   = "S-II"
)

const (
	columnStatus         = "status"
	columnNextAttemptAt  = "next_attempt_at"
	columnAttempt        = "attempt"
	columnUpdatedAt      = "updated_at"
	columnExposurePlanID = "exposureplan_id"
	columnFittedKey      = "fitted_key"
	columnFitReference   = "fit_reference"
	columnFitOffset      = "fit_offset"
	columnFitScale       = "fit_scale"
	columnFitSignature   = "fit_signature"
	columnFitError       = "fit_error"
)

const (
	keywordSIMPLE   = "SIMPLE"
	keywordBITPIX   = "BITPIX"
	keywordEXTEND   = "EXTEND"
	keywordBZERO    = "BZERO"
	keywordBSCALE   = "BSCALE"
	keywordROWORDER = "ROWORDER"
)

const (
	contentTypeFITS        = "application/fits"
	contentTypeOctetStream = "application/octet-stream"
	contentTypeJPEG        = "image/jpeg"
	contentEncodingGzip    = "gzip"
)

const solveDownscale = " -downscale"
