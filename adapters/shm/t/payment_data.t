#!/usr/bin/env perl
use strict;
use warnings;

use FindBin qw($Bin);
use lib "$Bin/../lib";

use Test::More;
use VFFFiscal::PaymentData qw(extract_fiscal_payment);

sub yookassa_payment {
    my (%override) = @_;
    my $payment = {
        pay_system_id => 'yookassa',
        user_id       => 19,
        money         => '10.00',
        comment       => {
            object => {
                paid   => 1,
                status => 'succeeded',
                captured_at => '2026-07-08T10:48:55Z',
                amount => { value => '10.00', currency => 'RUB' },
            },
        },
    };
    $payment->{$_} = $override{$_} for keys %override;
    return $payment;
}

sub platega_payment {
    my (%comment) = @_;
    my $comment_hash = {
        provider          => 'platega',
        transaction_id    => '11111111-1111-1111-1111-111111111111',
        status            => 'CONFIRMED',
        provider_status   => 'CONFIRMED',
        amount            => '150.00',
        amount_kopecks    => 15000,
        currency          => 'RUB',
        brand_id          => 'vff',
        payload           => 'vpnbot:v2:vff:19:15000',
        observed_at       => '2026-09-24T15:04:05Z',
        provider_amount   => '162.00',
        provider_commission => '12.00',
    };
    $comment_hash->{$_} = $comment{$_} for keys %comment;
    return {
        pay_system_id => 'platega',
        user_id       => 19,
        money         => '150.00',
        comment       => $comment_hash,
    };
}

subtest 'yookassa amount and captured_at unchanged' => sub {
    my ( $fiscal, $error ) = extract_fiscal_payment( yookassa_payment() );
    ok( !$error, 'no error' );
    is( $fiscal->{amount},         '10.00', 'amount from object.amount.value' );
    is( $fiscal->{operation_time}, '2026-07-08T10:48:55Z', 'captured_at unchanged' );
};

subtest 'yookassa falls back to payment.money' => sub {
    my $payment = yookassa_payment();
    delete $payment->{comment}{object}{amount};
    my ( $fiscal, $error ) = extract_fiscal_payment($payment);
    ok( !$error, 'no error' );
    is( $fiscal->{amount}, '10.00', 'money fallback' );
};

subtest 'yookassa unpaid still rejected' => sub {
    my $payment = yookassa_payment();
    $payment->{comment}{object}{paid} = 0;
    my ( $fiscal, $error ) = extract_fiscal_payment($payment);
    ok( !$fiscal, 'no fiscal' );
    is( $error->{status}, 409, 'not paid' );
};

subtest 'platega confirmed uses service amount not provider total' => sub {
    my ( $fiscal, $error ) = extract_fiscal_payment( platega_payment() );
    ok( !$error, 'no error' ) or diag explain $error;
    is( $fiscal->{amount},         '150.00', 'service amount' );
    is( $fiscal->{operation_time}, '2026-09-24T15:04:05Z', 'observed_at' );
    ok( !exists $fiscal->{provider_amount}, 'provider total is not a fiscal field' );
};

subtest 'platega v1 checks user and brand and keeps shm amount' => sub {
    my ( $fiscal, $error ) = extract_fiscal_payment(
        platega_payment( payload => 'vpnbot:v1:vff:19' )
    );
    ok( !$error, 'no error' ) or diag explain $error;
    is( $fiscal->{amount}, '150.00', 'amount from shm and comment' );
};

subtest 'platega fail closed cases' => sub {
    my @cases = (
        [ 'amount mismatch', { amount => '162.00' }, 409, 'comment amount' ],
        [ 'kopecks mismatch', { amount_kopecks => 16200 }, 409, 'amount_kopecks' ],
        [ 'user mismatch', { payload => 'vpnbot:v2:vff:99:15000' }, 409, 'user mismatch' ],
        [ 'brand mismatch', { payload => 'vpnbot:v2:fc:19:15000' }, 409, 'brand mismatch' ],
        [ 'payload amount mismatch', { payload => 'vpnbot:v2:vff:19:16200' }, 409, 'payload amount' ],
        [ 'wrong currency', { currency => 'USD' }, 409, 'currency' ],
        [ 'missing observed_at', { observed_at => undef }, 400, 'observed_at is missing' ],
        [ 'invalid observed_at', { observed_at => '2026-09-24 15:04:05' }, 400, 'not a valid RFC3339' ],
    );
    for my $case (@cases) {
        my ( $label, $override, $status, $fragment ) = @$case;
        my ( $fiscal, $error ) = extract_fiscal_payment( platega_payment(%$override) );
        ok( !$fiscal, "$label yields no fiscal data" );
        is( $error->{status}, $status, "$label status" );
        like( $error->{msg}, qr/\Q$fragment\E/, "$label message" );
    }
};

subtest 'chargebacked positive payment is skipped' => sub {
    for my $field (qw(status provider_status)) {
        my ( $fiscal, $error ) = extract_fiscal_payment( platega_payment( $field => 'CHARGEBACKED' ) );
        ok( !$error, "$field skip has no error" );
        is( $fiscal->{skip}, 1, "$field skipped" );
        is( $fiscal->{amount}, undef, "$field has no amount" );
    }
};

done_testing;
