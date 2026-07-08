#!/usr/bin/env perl
use strict;
use warnings;

use FindBin qw($Bin);
use lib "$Bin/../lib";

use JSON::PP ();
use Test::More;
use VFFFiscal::PaymentTimestamp qw(
    is_valid_rfc3339_timestamp
    is_paid_true
    extract_operation_time
);

sub assert_no_exception {
    my ( $code, $label ) = @_;
    my $ok = eval { $code->(); 1 };
    ok( $ok, "$label does not throw" );
    return $ok;
}

sub assert_extract_error {
    my ( $object, $expected_status, $label ) = @_;
    my ( $operation_time, $error );
    assert_no_exception(
        sub {
            ( $operation_time, $error ) = extract_operation_time($object);
        },
        $label
    );
    ok( !defined $operation_time, "$label returns no operation_time" );
    is( $error->{status}, $expected_status, "$label status" );
}

sub valid_payment_object {
    my (%overrides) = @_;
    return {
        paid        => JSON::PP::true,
        status      => 'succeeded',
        captured_at => '2026-07-08T10:48:55Z',
        %overrides,
    };
}

subtest 'is_valid_rfc3339_timestamp valid' => sub {
    my @valid = (
        '2026-07-08T10:48:55Z',
        '2026-07-08T10:48:55.751Z',
        '2026-07-08T13:48:55+03:00',
        '2026-07-08T13:48:55.123456+03:00',
        '2026-07-08T02:48:55-05:00',
        '2026-07-08T23:59:59+14:00',
    );
    for my $value (@valid) {
        ok( is_valid_rfc3339_timestamp($value), "valid: $value" );
        assert_no_exception( sub { is_valid_rfc3339_timestamp($value) }, "valid no throw: $value" );
    }
};

subtest 'is_valid_rfc3339_timestamp invalid' => sub {
    my @invalid = (
        [ undef, 'undef' ],
        [ '', 'empty' ],
        [ '2026-07-08T10:48:55', 'missing timezone' ],
        [ '2026-07-08 10:48:55Z', 'space separator' ],
        [ '2026-07-08T10:48:55.', 'malformed fractional seconds' ],
        [ '2026-07-08T10:48:55..751Z', 'malformed fractional seconds' ],
        [ '2026-13-08T10:48:55Z', 'month outside 01..12' ],
        [ '2026-07-08T24:48:55Z', 'hour outside 00..23' ],
        [ '2026-07-08T10:60:55Z', 'minute outside 00..59' ],
        [ '2026-07-08T10:48:60Z', 'second outside 00..59' ],
        [ '2026-07-08T10:48:55+03:61', 'timezone minute outside 00..59' ],
        [ '2026-07-08T10:48:55+14:01', 'timezone offset greater than 14:00' ],
        [ '2026-07-08T10:48:55Z trailing', 'trailing garbage' ],
    );
    for my $case (@invalid) {
        my ( $value, $label ) = @$case;
        ok( !is_valid_rfc3339_timestamp($value), "invalid: $label" );
        assert_no_exception(
            sub { is_valid_rfc3339_timestamp($value) },
            "invalid no throw: $label"
        );
    }
};

subtest 'is_paid_true' => sub {
    ok( is_paid_true(JSON::PP::true), 'JSON true is paid' );
    ok( is_paid_true(1), 'numeric 1 is paid' );
    ok( !is_paid_true(JSON::PP::false), 'JSON false is not paid' );
    ok( !is_paid_true(0), 'numeric 0 is not paid' );
    ok( !is_paid_true(undef), 'undef is not paid' );
    ok( !is_paid_true('false'), 'string false is not paid' );
    ok( !is_paid_true('0'), 'string 0 is not paid' );
    ok( !is_paid_true('yes'), 'arbitrary string is not paid' );
};

subtest 'extract_operation_time valid' => sub {
    my @cases = (
        [ '2026-07-08T10:48:55Z', { paid => JSON::PP::true } ],
        [ '2026-07-08T10:48:55.751Z', { captured_at => '2026-07-08T10:48:55.751Z' } ],
        [ '2026-07-08T13:48:55+03:00', { captured_at => '2026-07-08T13:48:55+03:00' } ],
        [ '2026-07-08T02:48:55-05:00', { captured_at => '2026-07-08T02:48:55-05:00' } ],
        [ '2026-07-08T10:48:55Z', { paid => 1 } ],
    );

    for my $case (@cases) {
        my ( $expected, $overrides ) = @$case;
        my ( $operation_time, $error ) = extract_operation_time( valid_payment_object(%$overrides) );
        is( $error, undef, "valid object has no error for $expected" );
        is( $operation_time, $expected, "returns captured_at unchanged for $expected" );
    }
};

subtest 'extract_operation_time invalid object' => sub {
    assert_extract_error( undef, 400, 'undef object' );
    assert_extract_error( 'scalar', 400, 'scalar object' );
    assert_extract_error( [], 400, 'array object' );
    assert_extract_error( {}, 409, 'empty hash object' );
};

subtest 'extract_operation_time invalid paid' => sub {
    for my $paid ( 0, undef, 'false', '0', 'yes' ) {
        my $label = 'paid=' . ( defined $paid ? $paid : 'undef' );
        assert_extract_error(
            valid_payment_object( paid => $paid ),
            409,
            $label
        );
    }
};

subtest 'extract_operation_time invalid status' => sub {
    for my $status ( undef, '', 'pending', 'canceled' ) {
        assert_extract_error(
            valid_payment_object( status => $status ),
            409,
            "status=" . ( defined $status ? $status : 'undef' )
        );
    }
};

subtest 'extract_operation_time invalid captured_at' => sub {
    my @invalid = (
        [ valid_payment_object( captured_at => undef ), 'undef captured_at' ],
        [ valid_payment_object( captured_at => '' ), 'empty captured_at' ],
        [ valid_payment_object( captured_at => '2026-07-08T10:48:55' ), 'no timezone' ],
        [ valid_payment_object( captured_at => '2026-07-08 10:48:55Z' ), 'malformed date/time' ],
        [ valid_payment_object( captured_at => '2026-07-08T10:48:55+14:01' ), 'invalid offset' ],
        [ valid_payment_object( captured_at => '2026-07-08T10:48:55Zextra' ), 'trailing garbage' ],
    );

    for my $case (@invalid) {
        my ( $object, $label ) = @$case;
        assert_extract_error( $object, 400, $label );
    }

    assert_extract_error(
        { paid => JSON::PP::true, status => 'succeeded' },
        400,
        'missing captured_at key'
    );
};

done_testing();
