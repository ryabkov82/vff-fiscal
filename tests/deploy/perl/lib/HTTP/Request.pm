package HTTP::Request;

use strict;
use warnings;

sub new {
    my ( $class, $method, $url ) = @_;
    return bless {
        _method  => $method,
        _url     => $url,
        _headers => {},
    }, $class;
}

sub header {
    my ( $self, $key, $value ) = @_;
    if ( defined $value ) {
        $self->{_headers}{$key} = $value;
    }
    return $self->{_headers}{$key};
}

1;
