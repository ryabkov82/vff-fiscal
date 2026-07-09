package Core::Config;

use strict;
use warnings;

our %STORE;

sub reset_state {
    %STORE = ();
}

sub set_value {
    my ( $key, $value ) = @_;
    $STORE{$key} = _copy_hashref($value);
    return $value;
}

sub _copy_hashref {
    my ($value) = @_;
    return {} unless $value && ref $value eq 'HASH';

    my %copy;
    for my $key ( keys %{$value} ) {
        my $entry = $value->{$key};
        $copy{$key} = ref $entry eq 'HASH' ? { %{$entry} } : $entry;
    }
    return \%copy;
}

1;
