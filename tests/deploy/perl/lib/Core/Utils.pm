package Core::Utils;

use strict;
use warnings;

use JSON::PP ();
use Exporter qw(import);

our @EXPORT_OK = qw(encode_json decode_json);

sub encode_json {
    my ($value) = @_;
    return JSON::PP::encode_json($value);
}

sub decode_json {
    my ($value) = @_;
    return JSON::PP::decode_json($value);
}

1;
