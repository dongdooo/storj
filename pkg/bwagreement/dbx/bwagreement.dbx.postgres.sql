-- AUTOGENERATED BY gopkg.in/spacemonkeygo/dbx.v1
-- DO NOT EDIT
CREATE TABLE bwagreements (
	signature bytea NOT NULL,
	data bytea NOT NULL,
	created_at timestamp with time zone NOT NULL,
	PRIMARY KEY ( signature )
);