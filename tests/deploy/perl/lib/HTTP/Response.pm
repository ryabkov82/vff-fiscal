package HTTP::Response;

use strict;
use warnings;

sub new {
    my ( $class, $code, $msg, $headers, $content ) = @_;
    return bless {
        _code    => $code,
        _msg     => $msg // '',
        _content => $content // '',
    }, $class;
}

sub is_success {
    my ($self) = @_;
    return $self->{_code} >= 200 && $self->{_code} < 300;
}

sub code {
    return $_[0]->{_code};
}

sub status_line {
    my ($self) = @_;
    return "$self->{_code} $self->{_msg}";
}

sub decoded_content {
    return $_[0]->{_content};
}

1;
