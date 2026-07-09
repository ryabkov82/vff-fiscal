package VFFFiscal::AdapterConfig;

use strict;
use warnings;

use Exporter qw(import);

our @EXPORT_OK = qw(
    normalize_non_empty_scalar
    resolve_api_token
);

sub normalize_non_empty_scalar {
    my ($value) = @_;

    return if !defined $value;
    return if ref $value;

    $value =~ s/\A\s+|\s+\z//g;
    return if !length $value;

    return $value;
}

sub resolve_api_token {
    my ( $config_token, $environment_token ) = @_;

    my $normalized_config = normalize_non_empty_scalar($config_token);
    return $normalized_config if defined $normalized_config;

    return normalize_non_empty_scalar($environment_token);
}

1;
