#!/usr/bin/env perl
use strict;
use warnings;

use FindBin qw($Bin);
use lib "$Bin/../lib";

use Test::More;
use VFFFiscal::AdapterConfig qw(
    normalize_non_empty_scalar
    resolve_api_token
);

my $CONFIG_TOKEN  = 'config-token-value';
my $ENVIRONMENT_TOKEN = 'environment-token-value';
my $ARBITRARY_TOKEN = 'legacy-random-token-format!@#';

subtest 'normalize_non_empty_scalar' => sub {
    is( normalize_non_empty_scalar(' abc '), 'abc', 'trims outer whitespace' );
    is( normalize_non_empty_scalar($ARBITRARY_TOKEN), $ARBITRARY_TOKEN,
        'accepts arbitrary non-empty token strings unchanged except trimming' );

    ok( !defined normalize_non_empty_scalar(undef), 'undef is rejected' );
    ok( !defined normalize_non_empty_scalar(''), 'empty string is rejected' );
    ok( !defined normalize_non_empty_scalar('   '), 'whitespace-only string is rejected' );
    ok( !defined normalize_non_empty_scalar( {} ), 'hash reference is rejected' );
    ok( !defined normalize_non_empty_scalar( [] ), 'array reference is rejected' );
};

subtest 'resolve_api_token precedence and fallback' => sub {
    is(
        resolve_api_token( $CONFIG_TOKEN, $ENVIRONMENT_TOKEN ),
        $CONFIG_TOKEN,
        'config token is used when valid'
    );
    is(
        resolve_api_token( " $CONFIG_TOKEN ", $ENVIRONMENT_TOKEN ),
        $CONFIG_TOKEN,
        'config token has precedence over environment token'
    );
    is(
        resolve_api_token( undef, $ENVIRONMENT_TOKEN ),
        $ENVIRONMENT_TOKEN,
        'environment token is used when config token is missing'
    );
    is(
        resolve_api_token( '', $ENVIRONMENT_TOKEN ),
        $ENVIRONMENT_TOKEN,
        'environment token is used when config token is empty'
    );
    is(
        resolve_api_token( '   ', $ENVIRONMENT_TOKEN ),
        $ENVIRONMENT_TOKEN,
        'whitespace-only config token falls back to environment'
    );
    is(
        resolve_api_token( undef, " $ENVIRONMENT_TOKEN " ),
        $ENVIRONMENT_TOKEN,
        'environment token is trimmed'
    );
    ok(
        !defined resolve_api_token( '   ', '   ' ),
        'whitespace-only values are rejected'
    );
    ok(
        !defined resolve_api_token( undef, undef ),
        'undef values are rejected'
    );
    is(
        resolve_api_token( {}, $ENVIRONMENT_TOKEN ),
        $ENVIRONMENT_TOKEN,
        'environment token is used when config token is a hash reference'
    );
    is(
        resolve_api_token( [], $ENVIRONMENT_TOKEN ),
        $ENVIRONMENT_TOKEN,
        'environment token is used when config token is an array reference'
    );
    ok(
        !defined resolve_api_token( {}, [] ),
        'invalid config and environment tokens are rejected'
    );
    is(
        resolve_api_token( '0', undef ),
        '0',
        'single-character zero token is accepted'
    );
};

done_testing();
