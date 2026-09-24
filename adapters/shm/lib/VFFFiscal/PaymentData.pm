package VFFFiscal::PaymentData;

use strict;
use warnings;

use Exporter qw(import);
use VFFFiscal::PaymentTimestamp qw(
    extract_operation_time
    is_valid_rfc3339_timestamp
);

our @EXPORT_OK = qw(extract_fiscal_payment);

# Returns (fiscal, error).
# fiscal is undef when error is set, or a hash:
#   { skip => 1, msg => '...' }
#   { amount => '150.00', operation_time => '...' }
# YooKassa and every non-platega system keep the historical extraction path.
# Platega fiscal amount is payment.money only.
sub extract_fiscal_payment {
    my ($payment) = @_;

    unless ( $payment && ref $payment eq 'HASH' ) {
        return ( undef, { status => 400, msg => 'Error: payment is missing or invalid' } );
    }

    my $pay_system = $payment->{pay_system_id};
    if ( defined $pay_system && !ref($pay_system) && $pay_system eq 'platega' ) {
        return extract_platega($payment);
    }

    return extract_legacy($payment);
}

sub extract_legacy {
    my ($payment) = @_;

    my $amount;
    my $object;
    if ( ref( $payment->{comment} ) eq 'HASH' ) {
        $object = $payment->{comment}{object};
        if ( ref($object) eq 'HASH' ) {
            my $amount_obj = $object->{amount};
            if ( ref($amount_obj) eq 'HASH' && defined $amount_obj->{value} && length $amount_obj->{value} ) {
                $amount = $amount_obj->{value};
            }
        }
    }
    unless (defined $amount) {
        $amount = $payment->{money};
    }
    unless ( $amount && $amount > 0 ) {
        return ( undef, { status => 400, msg => 'Error: payment amount is zero or negative' } );
    }

    my ( $operation_time, $timestamp_error ) = extract_operation_time($object);
    if ($timestamp_error) {
        return ( undef, $timestamp_error );
    }

    return (
        {
            amount         => sprintf( '%.2f', $amount ),
            operation_time => $operation_time,
        },
        undef
    );
}

sub extract_platega {
    my ($payment) = @_;

    my $comment = $payment->{comment};
    unless ( ref $comment eq 'HASH' ) {
        return ( undef, { status => 400, msg => 'Error: platega payment comment is missing or invalid' } );
    }

    my $status          = scalar_text( $comment->{status} );
    my $provider_status = scalar_text( $comment->{provider_status} );
    if ( ( defined $status && $status eq 'CHARGEBACKED' )
        || ( defined $provider_status && $provider_status eq 'CHARGEBACKED' ) )
    {
        return ( { skip => 1, msg => 'Skipped: platega payment is chargebacked' }, undef );
    }

    my $money_kopecks = money_to_kopecks( $payment->{money} );
    unless ( defined $money_kopecks && $money_kopecks > 0 ) {
        return ( undef, { status => 400, msg => 'Error: payment amount is zero or negative' } );
    }

    unless ( scalar_text( $comment->{provider} ) eq 'platega' ) {
        return ( undef, { status => 409, msg => 'Error: platega payment provider mismatch' } );
    }
    unless ( defined $status && $status eq 'CONFIRMED' ) {
        return ( undef, { status => 409, msg => 'Error: platega payment is not confirmed' } );
    }
    unless ( defined $provider_status && $provider_status eq 'CONFIRMED' ) {
        return ( undef, { status => 409, msg => 'Error: platega provider status is not confirmed' } );
    }
    unless ( scalar_text( $comment->{currency} ) eq 'RUB' ) {
        return ( undef, { status => 409, msg => 'Error: platega payment currency is not RUB' } );
    }
    my $transaction_id = scalar_text( $comment->{transaction_id} );
    unless ( defined $transaction_id && length $transaction_id ) {
        return ( undef, { status => 409, msg => 'Error: platega transaction_id is missing' } );
    }

    my $comment_kopecks = money_to_kopecks( $comment->{amount} );
    unless ( defined $comment_kopecks && $comment_kopecks == $money_kopecks ) {
        return ( undef, { status => 409, msg => 'Error: platega comment amount does not match payment' } );
    }

    my $stored_kopecks = integer_kopecks( $comment->{amount_kopecks} );
    unless ( defined $stored_kopecks && $stored_kopecks == $money_kopecks ) {
        return ( undef, { status => 409, msg => 'Error: platega amount_kopecks does not match payment' } );
    }

    my $payload_error = validate_platega_payload( $payment, $comment, $money_kopecks );
    return ( undef, $payload_error ) if $payload_error;

    my $observed_at = $comment->{observed_at};
    unless ( defined $observed_at && !ref($observed_at) && length $observed_at ) {
        return ( undef, { status => 400, msg => 'Error: observed_at is missing' } );
    }
    unless ( is_valid_rfc3339_timestamp($observed_at) ) {
        return ( undef, { status => 400, msg => 'Error: observed_at is not a valid RFC3339 timestamp' } );
    }

    return (
        {
            amount         => sprintf( '%.2f', $money_kopecks / 100 ),
            operation_time => $observed_at,
        },
        undef
    );
}

sub validate_platega_payload {
    my ( $payment, $comment, $money_kopecks ) = @_;

    my $payload = scalar_text( $comment->{payload} );
    unless ( defined $payload ) {
        return { status => 409, msg => 'Error: platega payload mismatch' };
    }

    my $user_id = positive_int( $payment->{user_id} );
    unless ( defined $user_id ) {
        return { status => 409, msg => 'Error: platega payload user mismatch' };
    }

    my $brand = scalar_text( $comment->{brand_id} );
    unless ( defined $brand && ( $brand eq 'vff' || $brand eq 'fc' ) ) {
        return { status => 409, msg => 'Error: platega payload brand mismatch' };
    }

    if ( $payload =~ /\Avpnbot:v2:(vff|fc):([1-9][0-9]{0,9}):([1-9][0-9]{0,9})\z/ ) {
        my ( $payload_brand, $payload_user, $payload_kopecks ) = ( $1, $2, $3 );
        unless ( $payload_brand eq $brand ) {
            return { status => 409, msg => 'Error: platega payload brand mismatch' };
        }
        unless ( 0 + $payload_user == $user_id && 0 + $payload_user <= 2147483647 ) {
            return { status => 409, msg => 'Error: platega payload user mismatch' };
        }
        unless ( 0 + $payload_kopecks == $money_kopecks && 0 + $payload_kopecks <= 2147483647 ) {
            return { status => 409, msg => 'Error: platega payload amount mismatch' };
        }
        return;
    }

    if ( $payload =~ /\Avpnbot:v1:(vff|fc):([1-9][0-9]{0,9})\z/ ) {
        my ( $payload_brand, $payload_user ) = ( $1, $2 );
        unless ( $payload_brand eq $brand ) {
            return { status => 409, msg => 'Error: platega payload brand mismatch' };
        }
        unless ( 0 + $payload_user == $user_id && 0 + $payload_user <= 2147483647 ) {
            return { status => 409, msg => 'Error: platega payload user mismatch' };
        }
        return;
    }

    return { status => 409, msg => 'Error: platega payload mismatch' };
}

sub scalar_text {
    my ($value) = @_;
    return if !defined $value || ref $value;
    $value =~ s/\A\s+|\s+\z//g;
    return if !length $value;
    return $value;
}

sub positive_int {
    my ($value) = @_;
    my $text = scalar_text($value);
    return if !defined $text || $text !~ /\A[1-9][0-9]{0,9}\z/;
    return if 0 + $text > 2147483647;
    return 0 + $text;
}

sub integer_kopecks {
    my ($value) = @_;
    return if !defined $value || ref $value;
    my $text = "$value";
    $text =~ s/\A\s+|\s+\z//g;
    return if $text !~ /\A[1-9][0-9]{0,9}\z/;
    return if 0 + $text > 2147483647;
    return 0 + $text;
}

# Strict money with at most two fractional digits. Returns integer kopecks.
sub money_to_kopecks {
    my ($value) = @_;
    return if !defined $value || ref $value;
    my $text = "$value";
    $text =~ s/\A\s+|\s+\z//g;
    return if $text !~ /\A(?:0|[1-9][0-9]*)(?:\.(\d{1,2}))?\z/;
    my $fraction = defined $1 ? $1 : '';
    $fraction .= '0' while length $fraction < 2;
    my ( $rubles, undef ) = split /\./, $text, 2;
    $rubles = 0 if !defined $rubles || $rubles eq '';
    my $kopecks = ( 0 + $rubles ) * 100 + ( 0 + $fraction );
    return if $kopecks > 2147483647;
    return $kopecks;
}

1;
