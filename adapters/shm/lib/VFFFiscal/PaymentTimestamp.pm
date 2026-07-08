package VFFFiscal::PaymentTimestamp;

use strict;
use warnings;

use Exporter qw(import);
use JSON::PP ();
use Time::Local qw(timegm_modern);

our @EXPORT_OK = qw(
    is_valid_rfc3339_timestamp
    is_paid_true
    extract_operation_time
);

# Validates RFC3339 structure and calendar/date-time ranges using timegm_modern.
# vff-fiscal performs the final strict parse before calling FNS.
sub is_valid_rfc3339_timestamp {
    my ($value) = @_;

    return 0 unless defined $value;
    return 0 if ref $value;
    return 0 unless length $value;

    return 0 unless $value =~ /\A
        (\d{4})-(\d{2})-(\d{2})T
        (\d{2}):(\d{2}):(\d{2})
        (?:\.(\d+))?
        (?:Z|([+-])(\d{2}):(\d{2}))
    \z/x;

    my ( $year, $month, $day, $hour, $minute, $second, $sign, $off_hour, $off_min ) =
        ( $1, $2, $3, $4, $5, $6, $8, $9, $10 );

    $month = 0 + $month;
    $day   = 0 + $day;
    $hour  = 0 + $hour;
    $minute = 0 + $minute;
    $second = 0 + $second;

    return 0 if $month < 1 || $month > 12;
    return 0 if $hour > 23;
    return 0 if $minute > 59 || $second > 59;

    if ( defined $sign ) {
        $off_hour = 0 + $off_hour;
        $off_min  = 0 + $off_min;
        return 0 if $off_min > 59;
        return 0 if $off_hour > 14;
        return 0 if $off_hour == 14 && $off_min > 0;
    }

    eval {
        timegm_modern( $second, $minute, $hour, $day, $month - 1, $year );
        1;
    } or return 0;

    return 1;
}

sub is_paid_true {
    my ($paid) = @_;

    return 0 unless defined $paid;

    if ( JSON::PP::is_bool($paid) ) {
        return $paid ? 1 : 0;
    }

    return 0 if ref $paid;
    return 1 if $paid =~ /\A1\z/;
    return 0;
}

sub extract_operation_time {
    my ($object) = @_;

    unless ( $object && ref $object eq 'HASH' ) {
        return ( undef, { status => 400, msg => 'Error: payment object is missing or invalid' } );
    }

    unless ( is_paid_true( $object->{paid} ) ) {
        return ( undef, { status => 409, msg => 'Error: payment is not paid' } );
    }

    my $status = $object->{status};
    unless ( defined $status && !ref($status) && length($status) && $status eq 'succeeded' ) {
        return ( undef, { status => 409, msg => 'Error: payment status is not succeeded' } );
    }

    my $captured_at = $object->{captured_at};
    unless ( defined $captured_at && !ref($captured_at) && length($captured_at) ) {
        return ( undef, { status => 400, msg => 'Error: captured_at is missing' } );
    }

    unless ( is_valid_rfc3339_timestamp($captured_at) ) {
        return ( undef, { status => 400, msg => 'Error: captured_at is not a valid RFC3339 timestamp' } );
    }

    return ( $captured_at, undef );
}

1;
